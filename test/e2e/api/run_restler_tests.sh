#!/usr/bin/env bash
# Runs RESTler versioning tests against deployed flightctl services.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}/../../.."
RESULTS_DIR="${ROOT_DIR}/reports/restler"
CLIENT_CONFIG="${HOME}/.config/flightctl/client.yaml"

log_info()  { echo "=== [restler-test] $* ==="; }
log_error() { echo "!!! [restler-test] ERROR: $* !!!" >&2; }

extract_server_url() {
    local key="^service:"
    [ "$1" = "imagebuilder" ] && key="^imageBuilderService:"
    awk -v key="$key" '
        $0 ~ key { found=1; next }
        found && /server:/ { sub(/.*server: */, ""); print; exit }
    ' "$CLIENT_CONFIG"
}

parse_host_port() {
    local stripped="${1#https://}"
    stripped="${stripped#http://}"
    stripped="${stripped%%/*}"
    local host="${stripped%%:*}"
    local port="443"
    if [[ "${stripped}" == *:* ]]; then
        port="${stripped##*:}"
    fi
    echo "$host" "$port"
}

run_restler_for_version() {
    local service="$1" version="$2" host="$3" port="$4"
    local version_results="${RESULTS_DIR}/${service}/${version}"
    mkdir -p "${version_results}"

    log_info "Running RESTler test for ${service}/${version} against ${host}:${port}"

    sed -e "s|__TARGET_HOST__|${host}|g" \
        -e "s|__TARGET_PORT__|${port}|g" \
        -e "s|__EXPECTED_VERSION__|${version}|g" \
        "${SCRIPT_DIR}/engine_settings.json" > "${version_results}/engine_settings.json"

    local enrollment_key="${ROOT_DIR}/bin/agent/etc/flightctl/certs/client-enrollment.key"
    if [ ! -f "${enrollment_key}" ]; then
        log_error "Missing required enrollment key: ${enrollment_key}"
        return 1
    fi

    local -a podman_args=(--rm --security-opt label=disable --network=host)
    local -a volumes=(
        -v "${ROOT_DIR}/api:/work/specs:ro"
        -v "${SCRIPT_DIR}:/work/config:ro"
        -v "${version_results}:/work/results"
        -v "${enrollment_key}:/work/certs/client-enrollment.key:ro"
    )
    podman_args+=(-e "RESTLER_TOKEN=${AUTH_TOKEN}")

    log_info "Compiling grammar for ${service}/${version}"
    podman run "${podman_args[@]}" "${volumes[@]}" "${RESTLER_IMAGE}" \
        dotnet /RESTler/restler/Restler.dll \
            --workingDirPath /work/results \
            compile \
            "/work/config/${service}/compiler_config_${version}.json"

    local grammar_dir
    grammar_dir=$(find "${version_results}" -name "grammar.py" -printf "%h" -quit 2>/dev/null || true)
    if [ -z "${grammar_dir}" ]; then
        log_error "grammar.py not found after compilation for ${service}/${version}"
        return 1
    fi
    local grammar_container="/work/results${grammar_dir#"${version_results}"}"

    local -a auth_args=(
        --token_refresh_interval 3600
        --token_refresh_command "bash /work/config/refresh_token.sh"
    )

    podman run "${podman_args[@]}" "${volumes[@]}" "${RESTLER_IMAGE}" \
        dotnet /RESTler/restler/Restler.dll \
            --workingDirPath /work/results \
            test \
            --grammar_file "${grammar_container}/grammar.py" \
            --dictionary_file "${grammar_container}/dict.json" \
            --settings "/work/results/engine_settings.json" \
            "${auth_args[@]}"

    log_info "RESTler test completed for ${service}/${version}"
}

collect_results() {
    local version_results="${RESULTS_DIR}/$1/$2"
    local failures=0

    echo ""
    log_info "Results for $1/$2"

    local f
    f=$(find "${version_results}" -name "testing_summary.json" -print -quit 2>/dev/null || true)
    if [ -n "$f" ]; then
        echo "--- Testing Summary ---"
        cat "$f"
        echo ""
    fi

    f=$(find "${version_results}" -name "bug_buckets.txt" -print -quit 2>/dev/null || true)
    if [ -n "$f" ] && [ -s "$f" ]; then
        echo "--- Bugs Found ---"
        cat "$f"
        echo ""
        failures=1
    else
        echo "No bugs found."
    fi

    local checker_logs
    checker_logs=$(find "${version_results}" -name "VersionChecker*" -print 2>/dev/null || true)
    if [ -n "${checker_logs}" ]; then
        echo "--- Version Checker Logs ---"
        for f in ${checker_logs}; do
            echo "  ${f}:"
            cat "${f}"
        done
        echo ""
    fi

    return ${failures}
}

main() {
    if [ ! -f "$CLIENT_CONFIG" ]; then
        log_error "Client config not found at ${CLIENT_CONFIG}. Run 'make deploy' first."
        exit 1
    fi

    AUTH_TOKEN=$(grep '^ *access-token:' "$CLIENT_CONFIG" | head -1 | sed 's/^ *access-token: *//')
    if [ -z "${AUTH_TOKEN}" ]; then
        log_error "No access token in ${CLIENT_CONFIG}. Log in first."
        exit 1
    fi
    log_info "Auth token extracted"

    rm -rf "${RESULTS_DIR}"
    mkdir -p "${RESULTS_DIR}"

    local overall_exit=0

    for service_dir in "${SCRIPT_DIR}"/*/; do
        local service
        service=$(basename "$service_dir")

        local dicts
        dicts=$(find "$service_dir" -name "dict_*.json" 2>/dev/null || true)
        [ -z "$dicts" ] && continue

        local server_url
        server_url=$(extract_server_url "$service")
        if [ -z "$server_url" ]; then
            log_error "Could not determine server URL for service '${service}'"
            overall_exit=1
            continue
        fi

        local host port
        read -r host port <<< "$(parse_host_port "$server_url")"
        log_info "Service '${service}' -> ${host}:${port}"

        for dict_file in ${dicts}; do
            local version
            version=$(basename "$dict_file" | sed 's/dict_//;s/\.json//')

            if ! run_restler_for_version "$service" "$version" "$host" "$port"; then
                overall_exit=1
            fi
            if ! collect_results "$service" "$version"; then
                overall_exit=1
            fi
        done
    done

    # Print summary and write markdown report
    python3 "${SCRIPT_DIR}/restler_report.py" "${RESULTS_DIR}"

    if [ $overall_exit -eq 0 ]; then
        log_info "All RESTler versioning tests passed"
    else
        log_error "Some RESTler versioning tests failed"
    fi
    exit ${overall_exit}
}

main "$@"

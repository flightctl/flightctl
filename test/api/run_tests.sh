#!/usr/bin/env bash
# Runs Schemathesis API tests against deployed flightctl services.
set -euo pipefail

CONTAINER_PID=""
trap 'echo "Interrupted"; [ -n "$CONTAINER_PID" ] && kill "$CONTAINER_PID" 2>/dev/null; exit 130' INT

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}/../.."
RESULTS_DIR="${ROOT_DIR}/reports/schemathesis"
CLIENT_CONFIG="${HOME}/.config/flightctl/client.yaml"
SCHEMATHESIS_IMAGE="${SCHEMATHESIS_IMAGE:-flightctl-schemathesis:latest}"

# Services and versions to test (override with SCHEMATHESIS_SUITES, comma-separated)
ALL_SERVICES="core/v1alpha1,core/v1beta1,imagebuilder/v1alpha1"
IFS=',' read -ra SERVICES <<< "${SCHEMATHESIS_SUITES:-$ALL_SERVICES}"
SERVICES=("${SERVICES[@]// /}")

log_info()  { echo "=== [schemathesis] $* ==="; }
log_info "Suites to test: ${SERVICES[*]}"
log_error() { echo "!!! [schemathesis] ERROR: $* !!!" >&2; }

extract_server_url() {
    local key="^service:"
    [ "$1" = "imagebuilder" ] && key="^imageBuilderService:"
    awk -v key="$key" '
        $0 ~ key { found=1; next }
        found && /server:/ { sub(/.*server: */, ""); print; exit }
    ' "$CLIENT_CONFIG"
}

run_schemathesis() {
    local service="$1" version="$2" server_url="$3" core_url="$4"
    local service_name="${service%%/*}"
    local version_results="${RESULTS_DIR}/${service}"
    mkdir -p "${version_results}"

    # Base URL: core specs use /api/v1 prefix, imagebuilder specs don't
    local base_url="${server_url}"
    [ "$service_name" != "imagebuilder" ] && base_url="${server_url}/api/v1"

    log_info "Running Schemathesis for ${service} against ${base_url}"

    local enrollment_key="${ROOT_DIR}/bin/agent/etc/flightctl/certs/client-enrollment.key"
    if [ ! -f "${enrollment_key}" ]; then
        log_error "Missing required enrollment key: ${enrollment_key}"
        return 1
    fi

    local spec_path="/app/specs/${service}/openapi.yaml"
    local config_path="/app/config/${service_name}/${version}/schemathesis.toml"

    local -a podman_args=(--rm --init --security-opt label=disable --network=host)
    [ -z "${CI:-}" ] && podman_args+=(-t)
    local -a volumes=(
        -v "${ROOT_DIR}/api:/app/specs:ro"
        -v "${SCRIPT_DIR}:/app/config:ro"
        -v "${version_results}:/app/results"
        -v "${enrollment_key}:/app/certs/client-enrollment.key:ro"
    )
    local -a env_args=(
        -e "PYTHONPATH=/app/config"
        -e "SCHEMATHESIS_TOKEN=${AUTH_TOKEN}"
        -e "BASE_URL=${base_url}"
        -e "CORE_URL=${core_url}"
        ${CI:+-e "CI=${CI}"}
        -e "SCHEMATHESIS_COVERAGE_REPORT_HTML_PATH=/app/results/schema-coverage.html"
    )

    log_info "Testing ${service}"
    podman run "${podman_args[@]}" "${volumes[@]}" "${env_args[@]}" \
        "${SCHEMATHESIS_IMAGE}" \
        "sh /app/config/run_suite.sh ${spec_path} ${config_path} /app/results" &
    CONTAINER_PID=$!
    local rc=0
    wait "$CONTAINER_PID" || rc=$?
    CONTAINER_PID=""

    log_info "Schemathesis completed for ${service}"
    return $rc
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

    # Get core server URL (needed for imagebuilder fixtures too)
    local core_url
    core_url=$(extract_server_url "core")
    if [ -z "${core_url}" ]; then
        log_error "Could not determine core server URL"
        exit 1
    fi

    rm -rf "${RESULTS_DIR}"
    mkdir -p "${RESULTS_DIR}"

    local overall_exit=0

    for entry in "${SERVICES[@]}"; do
        local service_name="${entry%%/*}"
        local version="${entry##*/}"

        local server_url
        server_url=$(extract_server_url "${service_name}")
        if [ -z "${server_url}" ]; then
            log_error "Could not determine server URL for service '${service_name}'"
            overall_exit=1
            continue
        fi
        log_info "Service '${service_name}' -> ${server_url}"

        if ! run_schemathesis "${entry}" "${version}" "${server_url}" "${core_url}"; then
            overall_exit=1
        fi
    done

    # Generate report (stdlib only, runs on host)
    log_info "Generating report"
    python3 "${SCRIPT_DIR}/report.py" "${RESULTS_DIR}" || true

    if [ $overall_exit -eq 0 ]; then
        log_info "All Schemathesis tests passed"
    else
        log_error "Some Schemathesis tests failed"
    fi
    exit ${overall_exit}
}

main "$@"

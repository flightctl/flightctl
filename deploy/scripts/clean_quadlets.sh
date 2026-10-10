#!/usr/bin/env bash

set -eo pipefail

# Load shared functions which contain the read-only directory constants
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

clean_services() {
    if [[ ${EUID} -ne 0 ]] && ! systemctl --user show-environment >/dev/null 2>&1; then
        echo "Warning: systemd user manager is unavailable; removing Flight Control containers through Podman"
        # Match only ContainerName values from the .container files installed
        # by servicesManifest; leave every other container in this Podman store.
        local generated_containers=(
            flightctl-api
            flightctl-periodic
            flightctl-worker
            flightctl-delta-worker
            flightctl-alert-exporter
            flightctl-pam-issuer
            flightctl-db
            flightctl-db-migrate
            flightctl-db-wait
            flightctl-db-users-init
            flightctl-kv
            flightctl-alertmanager
            flightctl-alertmanager-proxy
            flightctl-ui
            flightctl-ui-init
            flightctl-cli-artifacts
            flightctl-gateway
            flightctl-imagebuilder-api
            flightctl-imagebuilder-worker
            flightctl-remote-access
            flightctl-telemetry-gateway
            flightctl-prometheus
            flightctl-grafana
            flightctl-userinfo-proxy
        )
        local container
        local running_container
        while IFS= read -r container; do
            for running_container in "${generated_containers[@]}"; do
                [[ "$container" == "$running_container" ]] || continue
                # --force stops running containers before removing them. This clears
                # references that would otherwise prevent the named volume cleanup.
                podman rm --force "$container" || echo "Warning: Failed to remove container $container"
                break
            done
        done < <(podman ps --all --format '{{.Names}}' || true)
        return 0
    fi

    if [[ ${EUID} -ne 0 ]]; then
        systemctl --user import-environment XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME || echo "Warning: Failed to import XDG paths into the user systemd manager"
    fi

    # Stop any running services
    for service in flightctl.target flightctl-observability.target flightctl-*.service; do
        if run_systemctl is-active --quiet "$service"; then
            echo "Stopping $service..."
            run_systemctl stop "$service" || echo "Warning: Failed to stop service $service"
        fi
    done
}

rootless_path_is_on_parent_filesystem() {
    local path="$1"
    local parent
    local parent_device
    local path_device

    [[ -e "$path" || -L "$path" ]] || return 0
    parent="$(dirname -- "$path")"
    [[ -d "$parent" ]] || return 1
    parent_device="$(stat -c '%d' -- "$parent")" || return 1
    path_device="$(stat -c '%d' -- "$path")" || return 1
    if [[ "$parent_device" != "$path_device" ]]; then
        echo "Warning: Skipping $path because it is on a different filesystem than $parent" >&2
        return 1
    fi
}

reclaim_rootless_tree() {
    local path="$1"

    [[ -e "$path" || -L "$path" ]] || return 0
    if ! rootless_path_is_on_parent_filesystem "$path"; then
        return 1
    fi
    if [[ -L "$path" ]]; then
        return 0
    fi

    # Reclaim container-created ownership only within the target filesystem.
    podman unshare find "$path" -xdev -depth -exec chown 0:0 -- {} +
}

remove_rootless_tree() {
    local path="$1"

    [[ -e "$path" || -L "$path" ]] || return 0
    if ! rootless_path_is_on_parent_filesystem "$path"; then
        return 1
    fi

    # -xdev avoids descending into nested mounts. The parent-device check above
    # also prevents find from treating a mounted cleanup root as its own device.
    find "$path" -xdev -depth -delete
}

clean_files() {
    if [[ ${EUID} -ne 0 ]]; then
        # Podman's :U option can change bind-mount ownership inside a rootless
        # user namespace. Container-created build files can use mapped IDs too.
        # Reclaim only within each generated directory's filesystem.
        local path
        for path in \
            "${VAR_LIB_OUTPUT_DIR}/flightctl/grafana" \
            "${VAR_LIB_OUTPUT_DIR}/flightctl/prometheus" \
            "${CONFIG_WRITEABLE_DIR}/pki/flightctl-grafana" \
            "${VAR_TMP_OUTPUT_DIR}/flightctl-builds" \
            "${VAR_TMP_OUTPUT_DIR}/flightctl-exports"; do
            if [[ -e "$path" ]] && ! reclaim_rootless_tree "$path"; then
                echo "Warning: Failed to restore rootless Quadlet path ownership for $path"
            fi
        done
    fi

    # Use the read-only directory constants from shared.sh
    echo "Removing read-only configuration files from ${CONFIG_READONLY_DIR}"
    if [[ ${EUID} -eq 0 ]]; then
        rm -rf "$CONFIG_READONLY_DIR" || echo "Warning: Failed to remove read-only config files"
    elif ! remove_rootless_tree "$CONFIG_READONLY_DIR"; then
        echo "Warning: Failed to remove read-only config files"
    fi

    echo "Removing writeable configuration files from ${CONFIG_WRITEABLE_DIR}"
    if [[ ${EUID} -eq 0 ]]; then
        rm -rf "$CONFIG_WRITEABLE_DIR" || echo "Warning: Failed to remove writeable config files"
    elif [[ -d "$CONFIG_WRITEABLE_DIR" ]]; then
        # The CLI stores client.yaml and named contexts in this XDG directory
        # too. Keep those credentials while removing generated service state.
        if rootless_path_is_on_parent_filesystem "$CONFIG_WRITEABLE_DIR"; then
            local generated_path
            while IFS= read -r -d '' generated_path; do
                remove_rootless_tree "$generated_path" || echo "Warning: Failed to remove generated writeable config path $generated_path"
            done < <(find "$CONFIG_WRITEABLE_DIR" -xdev -mindepth 1 -maxdepth 1 \
                ! -name 'client.yaml' ! -name 'client_*.yaml' -print0)
        else
            echo "Warning: Skipping generated writable config removal because $CONFIG_WRITEABLE_DIR is on a different filesystem from its parent"
        fi
    fi

    echo "Removing quadlet files from ${QUADLET_FILES_OUTPUT_DIR}"
    if [[ ${EUID} -eq 0 ]]; then
        rm -rf "$QUADLET_FILES_OUTPUT_DIR/flightctl"* || echo "Warning: Failed to remove quadlet config files"
    else
        local generated_quadlet_files=(
            flightctl-api.container
            flightctl-periodic.container
            flightctl-worker.container
            flightctl-delta-worker.container
            flightctl-alert-exporter.container
            flightctl-pam-issuer.container
            flightctl-pam-issuer-etc.volume
            flightctl-db.container
            flightctl-db.volume
            flightctl-db-migrate.container
            flightctl-db-wait.container
            flightctl-db-users-init.container
            flightctl-kv.container
            flightctl-kv.volume
            flightctl-alertmanager.container
            flightctl-alertmanager.volume
            flightctl-alertmanager-proxy.container
            flightctl-ui.container
            flightctl-ui-init.container
            flightctl-ui-certs.volume
            flightctl-cli-artifacts.container
            flightctl-gateway.container
            flightctl-imagebuilder-api.container
            flightctl-imagebuilder-worker.container
            flightctl-remote-access.container
            flightctl-telemetry-gateway.container
            flightctl-prometheus.container
            flightctl-grafana.container
            flightctl-userinfo-proxy.container
            flightctl.network
            flightctl-listeners.volume
        )
        local filename
        for filename in "${generated_quadlet_files[@]}"; do
            rm -f -- "${QUADLET_FILES_OUTPUT_DIR}/${filename}" || echo "Warning: Failed to remove generated Quadlet file ${filename}"
        done

        local generated_dropins=(
            flightctl-gateway.container.d/10-api-host-port.conf
            flightctl-gateway.container.d/20-upstream-checks.conf
            flightctl-imagebuilder-worker.container.d/10-rootless-kvm.conf
            flightctl-imagebuilder-worker.container.d/20-build-storage.conf
            flightctl-grafana.container.d/99-rootless-state.conf
            flightctl-prometheus.container.d/99-rootless-state.conf
        )
        local dropin
        for dropin in "${generated_dropins[@]}"; do
            rm -f -- "${QUADLET_FILES_OUTPUT_DIR}/${dropin}" || echo "Warning: Failed to remove generated Quadlet drop-in ${dropin}"
        done

        rmdir --ignore-fail-on-non-empty \
            "${QUADLET_FILES_OUTPUT_DIR}/flightctl-gateway.container.d" \
            "${QUADLET_FILES_OUTPUT_DIR}/flightctl-imagebuilder-worker.container.d" \
            "${QUADLET_FILES_OUTPUT_DIR}/flightctl-grafana.container.d" \
            "${QUADLET_FILES_OUTPUT_DIR}/flightctl-prometheus.container.d" 2>/dev/null || true
    fi

    echo "Removing systemd unit files from ${SYSTEMD_UNIT_OUTPUT_DIR}"
    if [[ ${EUID} -eq 0 ]]; then
        rm -rf "$SYSTEMD_UNIT_OUTPUT_DIR/flightctl"* || echo "Warning: Failed to remove systemd unit files"
    else
        local generated_systemd_units=(
            flightctl-api-init.service
            flightctl-certs-init.service
            flightctl.target
            flightctl-observability.target
        )
        local unit
        for unit in "${generated_systemd_units[@]}"; do
            rm -f -- "${SYSTEMD_UNIT_OUTPUT_DIR}/${unit}" || echo "Warning: Failed to remove generated systemd unit ${unit}"
        done

        local generated_target_links=(
            default.target.wants/flightctl.target
            default.target.wants/flightctl-observability.target
        )
        local target_link
        for target_link in "${generated_target_links[@]}"; do
            rm -f -- "${SYSTEMD_UNIT_OUTPUT_DIR}/${target_link}" || echo "Warning: Failed to remove generated systemd target link ${target_link}"
        done
        rmdir --ignore-fail-on-non-empty "${SYSTEMD_UNIT_OUTPUT_DIR}/default.target.wants" 2>/dev/null || true
    fi

    local delta_dropin="${QUADLET_SYSTEMD_DIR}/flightctl-delta-worker.container.d/delta-generation-repository.conf"
    if [[ -f "$delta_dropin" ]] && grep -q '^# Generated by Flight Control deployment;' "$delta_dropin"; then
        echo "Removing generated delta-worker drop-in"
        rm -f "$delta_dropin"
    fi

    local registry_ca_dropins=(
        "${QUADLET_SYSTEMD_DIR}/flightctl-delta-worker.container.d/e2e-registry-ca.conf"
        "${QUADLET_SYSTEMD_DIR}/flightctl-worker.container.d/e2e-registry-ca.conf"
    )
    local dropin
    for dropin in "${registry_ca_dropins[@]}"; do
        if [[ -f "$dropin" ]]; then
            echo "Removing E2E registry CA mount drop-in: $dropin"
            rm -f "$dropin" || echo "Warning: Failed to remove E2E registry CA mount drop-in $dropin"
        fi
    done

    if [[ ${EUID} -ne 0 ]]; then
        rm -f "${BIN_OUTPUT_DIR}/flightctl-standalone"
        for path in \
            "${VAR_TMP_OUTPUT_DIR}/flightctl-builds" \
            "${VAR_TMP_OUTPUT_DIR}/flightctl-exports" \
            "${VAR_LIB_OUTPUT_DIR}/flightctl/grafana" \
            "${VAR_LIB_OUTPUT_DIR}/flightctl/prometheus"; do
            remove_rootless_tree "$path" || echo "Warning: Failed to remove rootless Quadlet path $path"
        done
    fi

    run_systemctl daemon-reload || echo "Warning: Failed to reload systemd after removing generated units"
}

clean_volumes() {
    # Remove volumes
    for volume in flightctl-db flightctl-listeners flightctl-worker-storage flightctl-api-certs flightctl-kv flightctl-ui-certs flightctl-cli-artifacts-certs flightctl-alertmanager flightctl-alertmanager-proxy flightctl-alert-exporter flightctl-pam-issuer-etc; do
        if podman volume inspect "$volume" >/dev/null 2>&1; then
            echo "Removing volume $volume"
            podman volume rm "$volume" || echo "Warning: Failed to remove volume $volume"
        fi
    done
}

clean_networks() {
    # Remove networks
    if podman network inspect flightctl >/dev/null 2>&1; then
        echo "Removing network"
        podman network rm flightctl || echo "Warning: Failed to remove network"
    fi
}

clean_secrets() {
    # Remove generated secrets
    secrets=("flightctl-postgresql-password" "flightctl-postgresql-master-password" "flightctl-postgresql-user-password" "flightctl-postgresql-migrator-password" "flightctl-kv-password" "flightctl-alertmanager-password" "flightctl-alertmanager-proxy-password" "flightctl-delta-generation-default-repository-username" "flightctl-delta-generation-default-repository-password")
    for secret in "${secrets[@]}"; do
        if  podman secret inspect "$secret" &>/dev/null; then
            echo "Removing secret $secret"
            podman secret rm "$secret" || echo "Warning: Failed to remove secret $secret"
        fi
    done
}

main() {
    echo "Starting cleanup"

    require_rootless_local_podman
    validate_runtime_output_paths

    clean_services
    clean_files
    clean_volumes
    clean_networks
    clean_secrets

    echo "Cleanup completed"
}

main

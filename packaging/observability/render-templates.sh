#!/bin/bash
# This is a logic library, not a standalone script.

render_templates() {
    local config_file="$1"
    local templates_dir="$2"
    local definitions_file="$3"

    set -eo pipefail

    LOG_TAG="flightctl-config-reloader"

    log() {
        local level="$1"
        shift
        logger -t "$LOG_TAG" "$level: $*"
        echo "$level: $*" >&2
    }

    require_bin() {
        command -v "$1" &>/dev/null || { log "ERROR" "Required binary '$1' not found"; exit 1; }
    }

    require_bin python3
    require_bin envsubst

    log "INFO" "Loading variable definitions from $definitions_file"

    declare -A RENDER_MAP
    declare -A SYSTEMD_SERVICES

    # Step 1: Parse definitions
    while IFS='|' read -r env_var config_path default_value template_file output_file; do
        env_var=$(echo "$env_var" | xargs)
        config_path=$(echo "$config_path" | xargs)
        default_value=$(echo "$default_value" | xargs)
        template_file=$(echo "$template_file" | xargs)
        output_file=$(echo "$output_file" | xargs)

        [[ "$env_var" =~ ^#.*$ || -z "$env_var" ]] && continue

        value=$(python3 /usr/share/flightctl/yaml_helpers.py extract "$config_path" "$config_file" --default "$default_value")

        export "$env_var"="$value"

        key="${template_file}__${output_file}"
        RENDER_MAP["$key"]="$template_file|$output_file"

        if [[ "$output_file" == *.container ]]; then
            svc_name=$(basename "$output_file" .container).service
            SYSTEMD_SERVICES["$svc_name"]=1
        fi
    done < "$definitions_file"

    # Step 2: Render all
    for pair in "${RENDER_MAP[@]}"; do
        IFS='|' read -r template_file output_file <<< "$pair"
        full_template_path="${templates_dir}/${template_file}"

        # Special handling for UserInfo proxy - skip if not configured
        if [[ "$template_file" == "flightctl-userinfo-proxy.container.template" ]]; then
            if [[ -z "${USERINFO_UPSTREAM_URL:-}" ]]; then
                log "INFO" "Skipping $template_file (USERINFO_UPSTREAM_URL not configured)"
                # Remove the service from the restart list
                unset SYSTEMD_SERVICES["flightctl-userinfo-proxy.service"]
                continue
            fi
        fi

        log "INFO" "Rendering $full_template_path -> $output_file"
        # envsubst is run twice to handle nested variables
        envsubst < "$full_template_path" | envsubst > "$output_file" || {
            log "ERROR" "Failed to render $output_file"
            exit 1
        }

        chmod 0644 "$output_file"
        /usr/sbin/restorecon "$output_file" 2>/dev/null || log "WARNING" "restorecon failed on $output_file"
    done

    # Restore SELinux context for Grafana datasources file if it exists
    if [[ -f "/etc/grafana/provisioning/datasources/prometheus.yaml" ]]; then
                 /usr/sbin/restorecon "/etc/grafana/provisioning/datasources/prometheus.yaml" 2>/dev/null || log "WARNING" "restorecon failed on prometheus.yaml"
    fi

    # Step 3: Reload systemd (always needed for new unit files)
    log "INFO" "Reloading systemd..."
    systemctl daemon-reload

    log "INFO" "Configuration templates rendered successfully"
}

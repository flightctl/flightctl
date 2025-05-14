#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh
source "${SCRIPT_DIR}"/init_utils_host.sh

SERVICE_CONFIG_FILE="/etc/flightctl/service-config.yaml"

write_default_base_domain() {
    # Write base domain to the config file
    base_domain="$(ip route get 1.1.1.1 | grep -oP 'src \K\S+')"
    echo "Setting base domain to: ${base_domain}"
    sed -i "s/^\(\s*baseDomain\s*\):\s*.*$/\1: ${base_domain}/" "${SERVICE_CONFIG_FILE}"
}

main() {
    echo "Configuring Flight Control"

    ensure_secrets

    base_domain=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")
    if [[ -z "$base_domain" ]]; then
        write_default_base_domain
    else
        echo "Base domain already set to: $base_domain"
    fi

    echo "Configuration complete"
}

main

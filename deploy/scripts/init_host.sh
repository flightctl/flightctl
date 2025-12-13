#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh
source "${SCRIPT_DIR}"/init_utils.sh

SERVICE_CONFIG_FILE="/etc/flightctl/service-config.yaml"

main() {
    echo "Configuring Flight Control"

    ensure_secrets

    # Validate the base domain from config, or default to hostname FQDN
    base_domain=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")
    if [[ -z "$base_domain" ]]; then
        base_domain=$(hostname -f || hostname)
        echo "global.baseDomain not set, defaulting to system hostname FQDN ($base_domain)"
    fi

    # Validate as hostname or FQDN: lowercase alphanumerics and hyphens, final label must start with letter
    if ! [[ "$base_domain" =~ ^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)*[a-z]([-a-z0-9]*[a-z0-9])?$ ]]; then
        echo "ERROR: global.baseDomain must be a valid hostname or FQDN (not an IP address)" 1>&2
        exit 1
    fi

    echo "Configuration complete"
}

main

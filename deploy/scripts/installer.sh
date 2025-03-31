#!/usr/bin/env bash

set -eo pipefail

# Define environment variables with defaults
# Configuration variables
: ${BASE_DOMAIN:=""}
: ${AUTH_TYPE:="none"}

# Directory path for templates
: ${TEMPLATE_DIR:="/etc/flightctl/templates"}

# Export variables needed by functions in shared.sh
export BASE_DOMAIN

# Load shared functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Configure authentication based on AUTH_TYPE
if [[ "$AUTH_TYPE" == "none" ]]; then
    export FLIGHTCTL_DISABLE_AUTH="true"
else
    unset FLIGHTCTL_DISABLE_AUTH
fi

set_defaults() {
    if [ -z "$BASE_DOMAIN" ] || [ -z "$(echo "$BASE_DOMAIN" | xargs)" ]; then
        # By default, use the external IP address as the base domain
        export BASE_DOMAIN="$(ip route get 1.1.1.1 | grep -oP 'src \K\S+')"
    fi
}

render_files() {
    render_service "api" "${TEMPLATE_DIR}"
    render_service "periodic" "${TEMPLATE_DIR}"
    render_service "worker" "${TEMPLATE_DIR}"
    render_service "db" "${TEMPLATE_DIR}"
    render_service "kv" "${TEMPLATE_DIR}"
    render_service "ui" "${TEMPLATE_DIR}"

    render_shared_files
}

main() {
    echo "Starting installation"

    set_defaults
    ensure_secrets
    render_files

    echo "Installation complete"
}

main

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

validate_inputs() {
    if [ -z "$BASE_DOMAIN" ] || [ -z "$(echo "$BASE_DOMAIN" | xargs)" ]; then
        echo "Error: BASE_DOMAIN is not set"
        exit 1
    fi
}

render_files() {
    # Render service configurations - passing TEMPLATE_DIR explicitly
    render_service "api" "${TEMPLATE_DIR}"
    render_service "periodic" "${TEMPLATE_DIR}"
    render_service "worker" "${TEMPLATE_DIR}"
    render_service "db" "${TEMPLATE_DIR}"
    render_service "kv" "${TEMPLATE_DIR}"
    render_service "ui" "${TEMPLATE_DIR}"

    # Copy the network and slice files
    cp "${TEMPLATE_DIR}/flightctl.network" "${QUADLET_FILES_OUTPUT_DIR}"
    cp "${TEMPLATE_DIR}/flightctl.slice" "${QUADLET_FILES_OUTPUT_DIR}"
}

main() {
    echo "Starting installation"

    validate_inputs
    ensure_secrets
    render_files

    echo "Installation complete"
}

main

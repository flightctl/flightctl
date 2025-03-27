#!/usr/bin/env bash

# Load shared code
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Conditionally set FLIGHTCTL_DISABLE_AUTH if AUTH_TYPE="none"
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
    # Copy the network and slice files
    mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"
    cp "${TEMPLATE_DIR}/flightctl.network" "${QUADLET_FILES_OUTPUT_DIR}"
    cp "${TEMPLATE_DIR}/flightctl.slice" "${QUADLET_FILES_OUTPUT_DIR}"

    render_service "api"
    render_service "periodic"
    render_service "worker"
    render_service "db"
    render_service "kv"
    render_service "ui"
}

# Execution

set -e

# TODO - handle certs
# TODO - handle auth
validate_inputs
ensure_secrets
render_files

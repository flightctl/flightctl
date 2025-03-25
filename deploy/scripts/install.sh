#!/usr/bin/env bash

set -eo pipefail

# Directory path for source files
: ${SOURCE_DIR:="deploy/podman"}

# Load shared functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Export directory paths so they're available to any subprocesses
export CONFIG_OUTPUT_DIR
export QUADLET_FILES_OUTPUT_DIR

render_files() {
    render_service "api" "${SOURCE_DIR}"
    render_service "periodic" "${SOURCE_DIR}"
    render_service "worker" "${SOURCE_DIR}"
    render_service "db" "${SOURCE_DIR}"
    render_service "kv" "${SOURCE_DIR}"
    render_service "ui" "${SOURCE_DIR}"

    move_shared_files "${SOURCE_DIR}"

    # Create directory for certs
    mkdir -p "${CONFIG_OUTPUT_DIR}/pki"
}

main() {
    echo "Starting installation"

    render_files

    echo "Installation complete"
}

main

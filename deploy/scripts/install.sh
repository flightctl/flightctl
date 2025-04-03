#!/usr/bin/env bash

set -eo pipefail

# Directory path for templates
: ${TEMPLATE_DIR:="deploy/podman"}

# Load shared functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Export directory paths so they're available to any subprocesses
export CONFIG_OUTPUT_DIR
export QUADLET_FILES_OUTPUT_DIR

render_files() {
    render_service "api" "${TEMPLATE_DIR}"
    render_service "periodic" "${TEMPLATE_DIR}"
    render_service "worker" "${TEMPLATE_DIR}"
    render_service "db" "${TEMPLATE_DIR}"
    render_service "kv" "${TEMPLATE_DIR}"
    render_service "ui" "${TEMPLATE_DIR}"

    move_shared_files "${TEMPLATE_DIR}"
}

main() {
    echo "Starting installation"

    render_files

    echo "Installation complete"
}

main

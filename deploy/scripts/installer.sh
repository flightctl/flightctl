: ${BASE_DONAIN:=""}
: ${AUTH_TYPE:="none"}

# Input and ouput directories
: ${TEMPLATE_DIR:="/etc/flightctl/templates"}
: ${CONFIG_OUTPUT_DIR:="$HOME/.config/flightctl"}
: ${QUADLET_FILES_OUTPUT_DIR:="$HOME/.config/containers/systemd"}

# Load functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

export CONFIG_OUTPUT_DIR
# Conditionally set FLIGHTCTL_DISABLE_AUTH if AUTH_TYPE="none"
if [[ "$AUTH_TYPE" == "none" ]]; then
    export FLIGHTCTL_DISABLE_AUTH="true"
else
    unset FLIGHTCTL_DISABLE_AUTH
fi

validate_inputs() {
    if [ -z "$BASE_DOMAIN" ]; then
        echo "Error: BASE_DOMAIN is not set"
        exit 1
    fi
}

inject_vars() {
    envsubst '$BASE_DOMAIN $CONFIG_OUTPUT_DIR' < "$1" > "$2"
}

render_service() {
    local service_name="$1"

    # Process container template
    inject_vars "${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}.container" "${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"

    # Ensure config output directory exists
    mkdir -p "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}"

    # Process all files in the config directory
    for config_file in "${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            inject_vars "$config_file" "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}/$(basename "$config_file")"
        fi
    done

    # Move any .volume file if it exists
    for volume in "${TEMPLATE_DIR}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            cp "$volume" "${QUADLET_FILES_OUTPUT_DIR}"
        fi
    done
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

validate_inputs
ensure_secrets
# TODO - handle certs
# TODO - handle auth
render_files

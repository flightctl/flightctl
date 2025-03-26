: ${BASE_DONAIN:=""}
: ${BASE_DOMAIN_TLS_CERT:=""}
: ${BASE_DOMAIN_TLS_KEY:=""}
: ${AUTH_TYPE:="none"}

# TODO fix this
# : ${TEMPLATE_DIR:="/etc/flightctl/templates"}
# : ${CONFIG_OUTPUT_DIR:="/etc/flightctl/config"}
# : ${QUADLET_FILES_OUTPUT_DIR:="/etc/containers/systemd/users"}
: ${TEMPLATE_DIR:="/home/dcrowder/Workspace/flightctl/deploy/podman"}
: ${CONFIG_OUTPUT_DIR:="/home/dcrowder/Workspace/flightctl/deploy/test-config"}
: ${QUADLET_FILES_OUTPUT_DIR:="/home/dcrowder/Workspace/flightctl/deploy/test-quadlets"}

# Load common functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
# TODO rename env,sh to utils or something else
source "${SCRIPT_DIR}"/env.sh

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

    # If tls cert and key are provided, both must be provided
    if [ -z "$BASE_DOMAIN_TLS_CERT" ] && [ -n "$BASE_DOMAIN_TLS_KEY" ]; then
        echo "Error: BASE_DOMAIN_TLS_CERT is set but BASE_DOMAIN_TLS_KEY is not set"
        exit 1
    elif [ -n "$BASE_DOMAIN_TLS_CERT" ] && [ -z "$BASE_DOMAIN_TLS_KEY" ]; then
        echo "Error: BASE_DOMAIN_TLS_KEY is set but BASE_DOMAIN_TLS_CERT is not set"
        exit 1
    fi
}

render_service() {
    local service_name="$1"

    # Process container template
    envsubst < "${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}.container" > "${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"

    # Ensure config output directory exists
    mkdir -p "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}"

    # Process all files in the config directory
    for config_file in "${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            envsubst < "$config_file" > "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}/$(basename "$config_file")"
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
render_files

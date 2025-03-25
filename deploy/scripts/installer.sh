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

template_api() {
    local api_template="${TEMPLATE_DIR}/flightctl-api/flightctl-api.container"
    local api_config="${TEMPLATE_DIR}/flightctl-api/flightctl-api-config/config.yaml.template"

    local output_template="${QUADLET_FILES_OUTPUT_DIR}/flightctl-api.container"
    local output_config="${CONFIG_OUTPUT_DIR}/flightctl-api/config.yaml"

    export CONFIG_OUTPUT_DIR
    # Conditionally set FLIGHTCTL_DISABLE_AUTH if AUTH_TYPE="none"
    if [[ "$AUTH_TYPE" == "none" ]]; then
        export FLIGHTCTL_DISABLE_AUTH="true"
    else
        unset FLIGHTCTL_DISABLE_AUTH
    fi

    # Ensure output directories exist
    mkdir -p "$(dirname "$output_template")"
    mkdir -p "$(dirname "$output_config")"

    # Ensure output files exist or create empty ones before overwriting
    : > "$output_template"
    : > "$output_config"

    # Process the container template
    envsubst '${CONFIG_OUTPUT_DIR} ${FLIGHTCTL_DISABLE_AUTH}' < "$api_template" > "$output_template"

    # Process the config template
    envsubst '${BASE_DOMAIN} ${FLIGHTCTL_DISABLE_AUTH}' < "$api_config" > "$output_config"
}

# Execution

set -e

validate_inputs
ensure_secrets
template_api


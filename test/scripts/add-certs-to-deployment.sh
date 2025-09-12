#!/bin/bash

set -euo pipefail

# Function to cleanup temporary files
cleanup() {
    if [ -n "${PATCH_FILE:-}" ] && [ -f "${PATCH_FILE}" ]; then
        rm -f "${PATCH_FILE}"
    fi
    if [ -n "${API_CONFIG_PATCH:-}" ] && [ -f "${API_CONFIG_PATCH}" ]; then
        rm -f "${API_CONFIG_PATCH}"
    fi
}

# Function to validate certificate file
validate_certificate() {
    local cert_file="$1"
    if ! openssl x509 -in "${cert_file}" -noout -text >/dev/null 2>&1; then
        echo "ERROR: ${cert_file} is not a valid X.509 certificate"
        return 1
    fi
    return 0
}

# Setup cleanup trap
trap cleanup EXIT

if [ "$#" -ne 1 ]; then
    echo "Usage: $0 <cert-directory>"
    echo "  cert-directory: Directory containing CA certificates (.pem files)"
    exit 1
fi
CERT_DIR="$1"
NAMESPACE=${NAMESPACE:-flightctl-external}
CONFIG_MAP_NAME="tpm-ca-certs"

echo "Checking for CA certificates in ${CERT_DIR}..."

# Find all files ending with .pem
echo "Searching for certificates matching pattern: *.pem"
mapfile -t CERT_FILES < <(find "${CERT_DIR}" -maxdepth 1 -type f -name "*.pem" 2>/dev/null || true)

if [ ${#CERT_FILES[@]} -eq 0 ]; then
    echo "ERROR: No CA certificates (*.pem) found in ${CERT_DIR}."
    echo "Please ensure the directory contains valid PEM certificate files."
    exit 1
fi

echo "Found ${#CERT_FILES[@]} certificate file(s):"
# Validate each certificate file
VALID_CERT_FILES=()
for cert_file in "${CERT_FILES[@]}"; do
    if validate_certificate "${cert_file}"; then
        echo " ✓ ${cert_file}"
        VALID_CERT_FILES+=("${cert_file}")
    else
        echo " ✗ ${cert_file} (invalid certificate, skipping)"
    fi
done

if [ ${#VALID_CERT_FILES[@]} -eq 0 ]; then
    echo "ERROR: No valid X.509 certificates found."
    exit 1
fi

# Use validated certificates from here on
CERT_FILES=("${VALID_CERT_FILES[@]}")

# Get existing keys from the ConfigMap, if it exists
EXISTING_KEYS=$(kubectl get configmap "${CONFIG_MAP_NAME}" -n "${NAMESPACE}" -o go-template='{{range $key, $value := .data}}{{$key}}{{"\n"}}{{end}}' 2>/dev/null || true)

NEW_CERT_FILES=()
if [ -z "$EXISTING_KEYS" ]; then
    echo "ConfigMap ${CONFIG_MAP_NAME} not found. Creating a new one."
    NEW_CERT_FILES=("${CERT_FILES[@]}")
    
    KUBECTL_CREATE_ARGS=()
    for cert_file in "${NEW_CERT_FILES[@]}"; do
        filename=$(basename "${cert_file}")
        KUBECTL_CREATE_ARGS+=( "--from-file=${filename}=${cert_file}" )
    done
    if ! kubectl create configmap "${CONFIG_MAP_NAME}" -n "${NAMESPACE}" "${KUBECTL_CREATE_ARGS[@]}"; then
        echo "ERROR: Failed to create ConfigMap ${CONFIG_MAP_NAME}"
        exit 1
    fi
else
    echo "Existing ConfigMap ${CONFIG_MAP_NAME} found. Checking for new certificates to add..."
    for cert_file in "${CERT_FILES[@]}"; do
        filename=$(basename "${cert_file}")
        if ! echo "${EXISTING_KEYS}" | grep -q "^${filename}$"; then
            echo " - Found new certificate to add: ${filename}"
            NEW_CERT_FILES+=("${cert_file}")
        fi
    done

    if [ ${#NEW_CERT_FILES[@]} -gt 0 ]; then
        echo "Patching ConfigMap with ${#NEW_CERT_FILES[@]} new certificate(s)..."
        
        # Create a temporary patch file with the new certificates
        PATCH_FILE=$(mktemp)

        echo '{"data":{' > "${PATCH_FILE}"
        first=true
        for cert_file in "${NEW_CERT_FILES[@]}"; do
            filename=$(basename "${cert_file}")
            echo "Adding certificate: ${filename}"
            if [ "$first" = true ]; then
                first=false
            else
                echo ',' >> "${PATCH_FILE}"
            fi
            printf '"%s":' "${filename}" >> "${PATCH_FILE}"
            jq -Rs . < "${cert_file}" >> "${PATCH_FILE}"
        done
        echo '}}' >> "${PATCH_FILE}"
        
        if ! kubectl patch configmap "${CONFIG_MAP_NAME}" -n "${NAMESPACE}" --type=merge --patch-file="${PATCH_FILE}"; then
            echo "ERROR: Failed to patch ConfigMap ${CONFIG_MAP_NAME}"
            exit 1
        fi
    else
        echo "All local certificates are already in the ConfigMap. No changes needed."
        exit 0
    fi
fi

# --- Update Deployment ---
echo "Updating flightctl-api deployment to use the TPM CA certificates..."

# Get all certificate files currently in the ConfigMap to build the full path list
ALL_CERT_FILES=$(kubectl get configmap "${CONFIG_MAP_NAME}" -n "${NAMESPACE}" -o go-template='{{range $key, $value := .data}}{{$key}}{{"\n"}}{{end}}' 2>/dev/null || true)

# Build complete list of paths for all certificates
TPM_CA_PATHS=()
while IFS= read -r filename; do
    if [ -n "$filename" ]; then
        TPM_CA_PATHS+=("/etc/flightctl/tpm-cas/${filename}")
    fi
done <<< "$ALL_CERT_FILES"

# Convert to JSON array format for API server config
TPM_CA_PATHS_JSON=$(printf '%s\n' "${TPM_CA_PATHS[@]}" | jq -R . | jq -s .)

echo "Ensuring deployment has TPM CA volume and volume mount..."

# Ensure volume exists
if ! kubectl get deployment flightctl-api -n "${NAMESPACE}" -o json | jq -e '.spec.template.spec.volumes[]? | select(.name=="tpm-ca-certs")' >/dev/null; then
  if ! kubectl patch deployment flightctl-api -n "${NAMESPACE}" --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/volumes/-",
    "value": {
      "name": "tpm-ca-certs",
      "configMap": {
        "name": "tpm-ca-certs"
      }
    }
  },
  {
    "op": "add",
    "path": "/spec/template/spec/containers/0/volumeMounts/-",
    "value": {
      "name": "tpm-ca-certs",
      "mountPath": "/etc/flightctl/tpm-cas",
      "readOnly": true
    }
  }
]' >/dev/null 2>&1; then
    echo "ERROR: Failed to add TPM CA volume/volumeMount to deployment flightctl-api"
    exit 1
  fi
else
  echo "Volume and volumeMount already present, skipping patch."
fi

# Update API server configuration with tpmCAPaths
echo "Updating API server configuration with TPM CA paths..."
echo "TPM CA paths: ${TPM_CA_PATHS_JSON}"

# Create a patch file for the API server config
API_CONFIG_PATCH=$(mktemp)

# Ensure API config ConfigMap exists
if ! kubectl get configmap flightctl-api-config -n "${NAMESPACE}" >/dev/null 2>&1; then
    echo "ERROR: ConfigMap 'flightctl-api-config' not found in namespace ${NAMESPACE}"
    exit 1
fi
# Get current config and add/update tpmCAPaths under service section
kubectl get configmap flightctl-api-config -n "${NAMESPACE}" -o jsonpath='{.data.config\.yaml}' > "${API_CONFIG_PATCH}"

# Use yq to properly set tpmCAPaths under the service section
yq eval ".service.tpmCAPaths = ${TPM_CA_PATHS_JSON}" -i "${API_CONFIG_PATCH}"

echo "Updated API configuration:"
yq eval '.service.tpmCAPaths' "${API_CONFIG_PATCH}"

# Apply the updated config
kubectl create configmap flightctl-api-config-new -n "${NAMESPACE}" --from-file=config.yaml="${API_CONFIG_PATCH}" --dry-run=client -o yaml | kubectl apply -f -
kubectl patch configmap flightctl-api-config -n "${NAMESPACE}" --type=merge --patch "$(kubectl get configmap flightctl-api-config-new -n "${NAMESPACE}" -o json | jq '{data}')"
kubectl delete configmap flightctl-api-config-new -n "${NAMESPACE}"

echo "Restarting flightctl-api deployment to pick up changes..."
kubectl rollout restart deployment/flightctl-api -n "${NAMESPACE}"

echo "Waiting for flightctl-api deployment to roll out..."
if ! kubectl rollout status deployment/flightctl-api -n "${NAMESPACE}" --timeout=300s; then
    echo "ERROR: Deployment rollout failed or timed out"
    exit 1
fi

echo "Successfully configured deployment with CA certificates."

#!/usr/bin/env bash
# setup_telemetry_gateway_certs.sh
# Automates cert provisioning for flightctl telemetry-gateway in dev (kind) and creates the required K8s secrets.
# Supports both Kubernetes and Podman deployment modes.

set -euo pipefail

# -------------------------
# Defaults (tweak as needed)
# -------------------------
MODE="${MODE:-kubernetes}"  # kubernetes or podman
NAMESPACE="${NAMESPACE:-flightctl-external}"
DEPLOYMENT="${DEPLOYMENT:-flightctl-telemetry-gateway}"
CTX="${CTX:-kind-kind}"
CERTS_DIR="${CERTS_DIR:-./certs}"
CSR_NAME="${CSR_NAME:-svc-telemetry-gateway}"
SIGNER_NAME="${SIGNER_NAME:-flightctl.io/server-svc}"
TLS_SECRET_NAME="${TLS_SECRET_NAME:-telemetry-gateway-tls}"
CA_SECRET_NAME="${CA_SECRET_NAME:-flightctl-ca-secret}"
KEY_FILE_DEFAULT="${KEY_FILE_DEFAULT:-${CERTS_DIR}/${CSR_NAME}.key}"
CSR_FILE_DEFAULT="${CSR_FILE_DEFAULT:-${CERTS_DIR}/${CSR_NAME}.csr}"
CRT_FILE_DEFAULT="${CRT_FILE_DEFAULT:-${CERTS_DIR}/${CSR_NAME}.crt}"
CA_FILE_DEFAULT="${CA_FILE_DEFAULT:-${CERTS_DIR}/ca.crt}"
EXPIRATION_SECONDS="${EXPIRATION_SECONDS:-8640000}" # 100 days
YAML_HELPERS_PATH="${YAML_HELPERS_PATH:-/usr/share/flightctl/yaml_helpers.py}"
FORCE_ROTATE="${FORCE_ROTATE:-false}"

# Podman-specific defaults (no container dependency)

# Reasonable SANs for dev (adjust with --sans if you need extras)
DEFAULT_SANS="DNS:localhost,DNS:${CSR_NAME},DNS:flightctl-telemetry-gateway.${NAMESPACE}.svc,DNS:flightctl-telemetry-gateway.${NAMESPACE}.svc.cluster.local,IP:127.0.0.1"

usage() {
  cat <<EOF
Usage: $0 [options]

Options:
  --mode <mode>               Deployment mode: kubernetes or podman (default: ${MODE})
  --namespace <ns>            Kubernetes namespace (default: ${NAMESPACE})
  --context <ctx>             kubectl context      (default: ${CTX})
  --deployment <name>         Collector deployment (default: ${DEPLOYMENT})
  --certs-dir <dir>           Where to store local cert artifacts (default: ${CERTS_DIR})
  --csr-name <name>           CSR resource name    (default: ${CSR_NAME})
  --signer <name>             flightctl signerName (default: ${SIGNER_NAME})
  --tls-secret <name>         TLS secret name      (default: ${TLS_SECRET_NAME})
  --ca-secret <name>          CA  secret name      (default: ${CA_SECRET_NAME})
  --expiration <seconds>      CSR expirationSeconds (default: ${EXPIRATION_SECONDS})
  --sans "<SAN1,SAN2,...>"    SubjectAltName entries (default: "${DEFAULT_SANS}")
  --yaml-helpers-path <path>  Path to yaml_helpers.py script (default: ${YAML_HELPERS_PATH})
  --force-rotate              Rotate even if secrets exist
  -h|--help                   Show this help

Notes:
- Kubernetes mode requires: kubectl, openssl, python3 (with PyYAML), flightctl CLI.
- Podman mode requires: podman, openssl, python3 (with PyYAML), flightctl CLI.
- In Podman mode, secrets are created using 'podman secret create' - no container dependency.
- In Podman mode, local certificate files are removed after creating secrets for security.
- Use --secret flags when creating containers to inject the created secrets.
- Assumes you're already logged in with ./bin/flightctl (the deploy script handled that).
- Script is idempotent: if secrets already exist, it exits 0 unless --force-rotate is set.
EOF
}

# -------------------------
# Parse args
# -------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2;;
    --namespace) NAMESPACE="$2"; shift 2;;
    --context) CTX="$2"; shift 2;;
    --deployment) DEPLOYMENT="$2"; shift 2;;
    --certs-dir) CERTS_DIR="$2"; shift 2;;
    --csr-name) CSR_NAME="$2"; shift 2;;
    --signer) SIGNER_NAME="$2"; shift 2;;
    --tls-secret) TLS_SECRET_NAME="$2"; shift 2;;
    --ca-secret) CA_SECRET_NAME="$2"; shift 2;;
    --expiration) EXPIRATION_SECONDS="$2"; shift 2;;
    --sans) SANS="$2"; shift 2;;
    --yaml-helpers-path) YAML_HELPERS_PATH="$2"; shift 2;;
    --force-rotate) FORCE_ROTATE=true; shift;;
    -h|--help) usage; exit 0;;
    *) echo "Unknown arg: $1"; usage; exit 1;;
  esac
done

# Derive file paths AFTER parsing (respects any env overrides if you add them later)
KEY_FILE="${KEY_FILE:-${CERTS_DIR}/${CSR_NAME}.key}"
CSR_FILE="${CSR_FILE:-${CERTS_DIR}/${CSR_NAME}.csr}"
CRT_FILE="${CRT_FILE:-${CERTS_DIR}/${CSR_NAME}.crt}"
CA_FILE="${CA_FILE:-${CERTS_DIR}/ca.crt}"
SANS="${SANS:-$DEFAULT_SANS}"

# -------------------------
# Pre-flight checks
# -------------------------
need() { command -v "$1" >/dev/null 2>&1 || { echo "Missing dependency: $1"; exit 1; }; }

# Common dependencies
need openssl
need python3

# Default to using the flightctl binary in the bin directory
if [[ -x ./bin/flightctl ]]; then
  PATH="$(pwd)/bin:${PATH}"
fi
need flightctl

# Check if yaml_helpers.py script exists
if [[ ! -f "${YAML_HELPERS_PATH}" ]]; then
  echo "ERROR: Could not find yaml_helpers.py script at ${YAML_HELPERS_PATH}"
  exit 1
fi

# Mode-specific dependencies and setup
if [[ "${MODE}" == "kubernetes" ]]; then
  need kubectl
  kubectl config use-context "${CTX}" >/dev/null 2>&1 || {
    echo "Failed to select context ${CTX}. Check 'kubectl config get-contexts'."; exit 1;
  }
  
  # Ensure namespace exists (deploy script should have created it, but be safe)
  kubectl get ns "${NAMESPACE}" >/dev/null 2>&1 || kubectl create namespace "${NAMESPACE}"
  
  # Ensure the deployment resource exists (it may be CrashLooping until we add certs; that's OK)
  if ! kubectl -n "${NAMESPACE}" get deploy "${DEPLOYMENT}" >/dev/null 2>&1; then
    echo "ERROR: Deployment ${DEPLOYMENT} not found in ns ${NAMESPACE}. Run the main deploy first."
    exit 1
  fi
  
  # If secrets already exist and NOT rotating, we're done.
  if kubectl -n "${NAMESPACE}" get secret "${TLS_SECRET_NAME}" >/dev/null 2>&1 \
     && kubectl -n "${NAMESPACE}" get secret "${CA_SECRET_NAME}"  >/dev/null 2>&1 \
     && [[ "${FORCE_ROTATE}" != "true" ]]; then
    echo "Secrets ${TLS_SECRET_NAME} and ${CA_SECRET_NAME} already exist in ${NAMESPACE}. Nothing to do."
    exit 0
  fi
  
  # If rotating, delete existing secrets (the rollout restart at the end will refresh pods).
  if [[ "${FORCE_ROTATE}" == "true" ]]; then
    kubectl -n "${NAMESPACE}" delete secret "${TLS_SECRET_NAME}" --ignore-not-found
    kubectl -n "${NAMESPACE}" delete secret "${CA_SECRET_NAME}"  --ignore-not-found
  fi
elif [[ "${MODE}" == "podman" ]]; then
  need podman
  
  # Check if Podman secrets already exist and NOT rotating, we're done.
  TLS_KEY_SECRET_NAME="${TLS_SECRET_NAME}-key"
  if podman secret exists "${TLS_SECRET_NAME}" >/dev/null 2>&1 \
     && podman secret exists "${TLS_KEY_SECRET_NAME}" >/dev/null 2>&1 \
     && podman secret exists "${CA_SECRET_NAME}" >/dev/null 2>&1 \
     && [[ "${FORCE_ROTATE}" != "true" ]]; then
    echo "Podman secrets ${TLS_SECRET_NAME}, ${TLS_KEY_SECRET_NAME}, and ${CA_SECRET_NAME} already exist. Nothing to do."
    exit 0
  fi
  
  # If rotating, remove existing Podman secrets
  if [[ "${FORCE_ROTATE}" == "true" ]]; then
    podman secret rm "${TLS_SECRET_NAME}" >/dev/null 2>&1 || true
    podman secret rm "${TLS_KEY_SECRET_NAME}" >/dev/null 2>&1 || true
    podman secret rm "${CA_SECRET_NAME}" >/dev/null 2>&1 || true
  fi
else
  echo "ERROR: Invalid mode '${MODE}'. Must be 'kubernetes' or 'podman'."
  exit 1
fi

mkdir -p "${CERTS_DIR}"

# -------------------------
# Generate key + CSR (ECDSA P-256)
# -------------------------
if [[ ! -f "${KEY_FILE}" ]] || [[ "${FORCE_ROTATE}" == "true" ]]; then
  openssl ecparam -genkey -name prime256v1 -out "${KEY_FILE}"
fi

# Create an OpenSSL config for SANs
OPENSSL_CFG="$(mktemp)"
trap 'rm -f "$OPENSSL_CFG"' EXIT
cat > "${OPENSSL_CFG}" <<EOF
[ req ]
distinguished_name = dn
prompt = no
req_extensions = v3_req

[ dn ]
CN = ${CSR_NAME}

[ v3_req ]
subjectAltName = ${SANS}
EOF

# Generate CSR
openssl req -new -key "${KEY_FILE}" -out "${CSR_FILE}" -config "${OPENSSL_CFG}"

# -------------------------
# Create/Apply flightctl CSR
# -------------------------
CSR_B64="$(base64 -w 0 < "${CSR_FILE}")"
CSR_YAML="${CERTS_DIR}/csr.yaml"

cat > "${CSR_YAML}" <<EOF
apiVersion: flightctl.io/v1alpha1
kind: CertificateSigningRequest
metadata:
  name: ${CSR_NAME}
spec:
  request: ${CSR_B64}
  signerName: ${SIGNER_NAME}
  usages: ["clientAuth", "serverAuth", "CA:false"]
  expirationSeconds: ${EXPIRATION_SECONDS}
EOF

# If CSR exists and we're rotating, delete it to avoid "immutable field" issues
if [[ "${FORCE_ROTATE}" == "true" ]]; then
  flightctl delete csr "${CSR_NAME}" >/dev/null 2>&1 || true
fi

# Apply/Upsert
flightctl apply -f "${CSR_YAML}"

# Approve (retry a bit in case controller reconciliation is slow)
tries=0
until flightctl approve "csr/${CSR_NAME}" >/dev/null 2>&1 || [[ $tries -ge 12 ]]; do
  sleep 5
  tries=$((tries+1))
done

# Fetch issued certificate from CSR status (retry a bit in case certificate is not yet populated)
CERT_B64=""
tries=0
until [[ -n "${CERT_B64}" && "${CERT_B64}" != "null" ]] || [[ $tries -ge 5 ]]; do
  CSR_YAML_TEMP="$(mktemp)"
  flightctl get "csr/${CSR_NAME}" -o yaml > "${CSR_YAML_TEMP}" 2>/dev/null || echo ""
  CERT_B64="$(python3 "${YAML_HELPERS_PATH}" extract ".status.certificate" "${CSR_YAML_TEMP}" --default "")"
  rm -f "${CSR_YAML_TEMP}"
  if [[ -z "${CERT_B64}" || "${CERT_B64}" == "null" ]]; then
    sleep 5
    tries=$((tries+1))
  fi
done

if [[ -z "${CERT_B64}" || "${CERT_B64}" == "null" ]]; then
  echo "ERROR: CSR ${CSR_NAME} was not issued. Check flightctl CSR status."
  exit 1
fi
echo "${CERT_B64}" | base64 -d > "${CRT_FILE}"

# -------------------------
# Fetch CA from enrollment config
# -------------------------
ENR_CONFIG_TEMP="$(mktemp)"
flightctl enrollmentconfig > "${ENR_CONFIG_TEMP}" 2>/dev/null || { echo "ERROR: Failed to get enrollment config."; exit 1; }
ENR_CA_B64="$(python3 "${YAML_HELPERS_PATH}" extract ".enrollment-service.service.certificate-authority-data" "${ENR_CONFIG_TEMP}" --default "")"
rm -f "${ENR_CONFIG_TEMP}"
if [[ -z "${ENR_CA_B64}" || "${ENR_CA_B64}" == "null" ]]; then
  echo "ERROR: Could not retrieve CA from 'flightctl enrollmentconfig'."
  exit 1
fi
echo "${ENR_CA_B64}" | base64 -d > "${CA_FILE}"

# -------------------------
# Mode-specific secret creation functions
# -------------------------
create_k8s_secrets() {
  # TLS secret (server cert+key)
  if kubectl -n "${NAMESPACE}" get secret "${TLS_SECRET_NAME}" >/dev/null 2>&1; then
    kubectl -n "${NAMESPACE}" delete secret "${TLS_SECRET_NAME}"
  fi
  kubectl -n "${NAMESPACE}" create secret tls "${TLS_SECRET_NAME}" \
    --cert="${CRT_FILE}" \
    --key="${KEY_FILE}"

  # CA secret (generic with ca.crt)
  if kubectl -n "${NAMESPACE}" get secret "${CA_SECRET_NAME}" >/dev/null 2>&1; then
    kubectl -n "${NAMESPACE}" delete secret "${CA_SECRET_NAME}"
  fi
  kubectl -n "${NAMESPACE}" create secret generic "${CA_SECRET_NAME}" \
    --from-file="ca.crt=${CA_FILE}"
}

create_podman_secrets() {
  echo "Creating Podman secrets..."
  
  # Create TLS secret (server certificate)
  if podman secret exists "${TLS_SECRET_NAME}" >/dev/null 2>&1; then
    podman secret rm "${TLS_SECRET_NAME}" >/dev/null 2>&1 || true
  fi
  podman secret create "${TLS_SECRET_NAME}" "${CRT_FILE}"
  
  # Create TLS key secret (server private key)
  TLS_KEY_SECRET_NAME="${TLS_SECRET_NAME}-key"
  if podman secret exists "${TLS_KEY_SECRET_NAME}" >/dev/null 2>&1; then
    podman secret rm "${TLS_KEY_SECRET_NAME}" >/dev/null 2>&1 || true
  fi
  podman secret create "${TLS_KEY_SECRET_NAME}" "${KEY_FILE}"
  
  # Create CA secret
  if podman secret exists "${CA_SECRET_NAME}" >/dev/null 2>&1; then
    podman secret rm "${CA_SECRET_NAME}" >/dev/null 2>&1 || true
  fi
  podman secret create "${CA_SECRET_NAME}" "${CA_FILE}"
  
  echo "Podman secrets created:"
  echo "  TLS cert secret: ${TLS_SECRET_NAME}"
  echo "  TLS key secret:  ${TLS_KEY_SECRET_NAME}"
  echo "  CA cert secret:  ${CA_SECRET_NAME}"
  
  # Remove local certificate files after creating secrets (security best practice)
  echo "Removing local certificate files for security..."
  rm -f "${CRT_FILE}" "${KEY_FILE}" "${CA_FILE}"
  echo "Local certificate files removed."
}

# Create secrets based on mode
if [[ "${MODE}" == "kubernetes" ]]; then
  create_k8s_secrets
elif [[ "${MODE}" == "podman" ]]; then
  create_podman_secrets
fi

# -------------------------
# Mode-specific restart functions
# -------------------------
restart_k8s_deployment() {
  kubectl -n "${NAMESPACE}" rollout restart "deployment/${DEPLOYMENT}"
  
  # Optionally wait for ready (helpful in CI/dev flows)
  echo "Waiting for ${DEPLOYMENT} to become Ready..."
  kubectl -n "${NAMESPACE}" rollout status "deployment/${DEPLOYMENT}" --timeout=180s
}

# No restart function needed for Podman mode - secrets are managed independently

# Restart based on mode
if [[ "${MODE}" == "kubernetes" ]]; then
  restart_k8s_deployment
elif [[ "${MODE}" == "podman" ]]; then
  echo "Podman secrets created. No restart needed - secrets are available for container creation."
fi

# Success message based on mode
if [[ "${MODE}" == "kubernetes" ]]; then
  echo "✅ Telemetry Gateway certs & secrets are in place."
  echo "   TLS secret: ${TLS_SECRET_NAME}  (server.crt/server.key)"
  echo "   CA  secret: ${CA_SECRET_NAME}   (ca.crt)"
  echo "   Namespace : ${NAMESPACE}"
  echo "   Deployment: ${DEPLOYMENT}"
elif [[ "${MODE}" == "podman" ]]; then
  echo "✅ Telemetry Gateway certs & secrets are in place."
  echo "   Podman secrets created:"
  echo "     TLS cert: ${TLS_SECRET_NAME}"
  echo "     TLS key:  ${TLS_SECRET_NAME}-key"
  echo "     CA cert:  ${CA_SECRET_NAME}"
  echo "   Usage: podman run --secret ${TLS_SECRET_NAME} --secret ${TLS_SECRET_NAME}-key --secret ${CA_SECRET_NAME} ..."
fi

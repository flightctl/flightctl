#!/usr/bin/env bash
# setup_telemetry_gateway_certs.sh
# Automates cert provisioning for flightctl telemetry-gateway in dev (kind) and creates the required K8s secrets.

set -euo pipefail

# -------------------------
# Defaults (tweak as needed)
# -------------------------
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
FORCE_ROTATE="${FORCE_ROTATE:-false}"

# Reasonable SANs for dev (adjust with --sans if you need extras)
DEFAULT_SANS="DNS:localhost,DNS:${CSR_NAME},DNS:flightctl-telemetry-gateway.${NAMESPACE}.svc,DNS:flightctl-telemetry-gateway.${NAMESPACE}.svc.cluster.local,IP:127.0.0.1"

usage() {
  cat <<EOF
Usage: $0 [options]

Options:
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
  --force-rotate              Rotate even if secrets exist
  -h|--help                   Show this help

Notes:
- Requires: kubectl, openssl, jq, yq, flightctl CLI.
- Assumes you're already logged in with ./bin/flightctl (the deploy script handled that).
- Script is idempotent: if secrets already exist, it exits 0 unless --force-rotate is set.
EOF
}

# -------------------------
# Parse args
# -------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
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
need kubectl
need openssl
need jq
need yq

if ! command -v flightctl >/dev/null 2>&1 && [[ -x ./bin/flightctl ]]; then
  PATH="$(pwd)/bin:${PATH}"
fi
need flightctl

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

# Fetch issued certificate from CSR status
CERT_B64="$(flightctl get "csr/${CSR_NAME}" -o yaml | yq '.status.certificate' || echo "")"
if [[ -z "${CERT_B64}" || "${CERT_B64}" == "null" ]]; then
  echo "ERROR: CSR ${CSR_NAME} was not issued. Check flightctl CSR status."
  exit 1
fi
echo "${CERT_B64}" | base64 -d > "${CRT_FILE}"

# -------------------------
# Fetch CA from enrollment config
# -------------------------
ENR_CA_B64="$(flightctl enrollmentconfig | yq -r '."enrollment-service".service."certificate-authority-data"' || echo "")"
if [[ -z "${ENR_CA_B64}" || "${ENR_CA_B64}" == "null" ]]; then
  echo "ERROR: Could not retrieve CA from 'flightctl enrollmentconfig'."
  exit 1
fi
echo "${ENR_CA_B64}" | base64 -d > "${CA_FILE}"

# -------------------------
# Create/Update K8s secrets
# -------------------------
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

# -------------------------
# Restart collector to pick up certs
# -------------------------
kubectl -n "${NAMESPACE}" rollout restart "deployment/${DEPLOYMENT}"

# Optionally wait for ready (helpful in CI/dev flows)
echo "Waiting for ${DEPLOYMENT} to become Ready..."
kubectl -n "${NAMESPACE}" rollout status "deployment/${DEPLOYMENT}" --timeout=180s

echo "✅ Telemetry Gateway certs & secrets are in place."
echo "   TLS secret: ${TLS_SECRET_NAME}  (server.crt/server.key)"
echo "   CA  secret: ${CA_SECRET_NAME}   (ca.crt)"
echo "   Namespace : ${NAMESPACE}"
echo "   Deployment: ${DEPLOYMENT}"

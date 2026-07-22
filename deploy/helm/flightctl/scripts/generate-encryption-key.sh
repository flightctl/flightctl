#!/bin/bash
set -euo pipefail

ENCRYPTION_DIR=""
CREATE_K8S_SECRETS="false"
NAMESPACE=""
INTERNAL_NAMESPACE=""

usage() {
    cat <<EOF
Usage: $0 --encryption-dir <directory> [options]

Generates an AES-256 encryption key for Flight Control data-at-rest encryption.

Required:
  --encryption-dir <dir>        Directory to store the encryption key

Optional:
  --create-k8s-secrets          Create Kubernetes secret using oc/kubectl
  --namespace <ns>              Kubernetes namespace (required if --create-k8s-secrets is set)
  --internal-namespace <ns>     Internal namespace to copy encryption key secret to (optional)
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --encryption-dir)
            [[ $# -ge 2 ]] || { echo "Error: --encryption-dir requires a value" >&2; usage; }
            ENCRYPTION_DIR="$2"
            shift 2
            ;;
        --create-k8s-secrets)
            CREATE_K8S_SECRETS="true"
            shift
            ;;
        --namespace)
            [[ $# -ge 2 ]] || { echo "Error: --namespace requires a value" >&2; usage; }
            NAMESPACE="$2"
            shift 2
            ;;
        --internal-namespace)
            [[ $# -ge 2 ]] || { echo "Error: --internal-namespace requires a value" >&2; usage; }
            INTERNAL_NAMESPACE="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

# Validate required arguments
if [[ -z "$ENCRYPTION_DIR" ]]; then
    echo "Error: --encryption-dir is required"
    usage
fi

if [[ "$CREATE_K8S_SECRETS" == "true" ]] && [[ -z "$NAMESPACE" ]]; then
    echo "Error: --namespace is required when --create-k8s-secrets is set"
    usage
fi

# Detect K8s CLI early (needed for restoring existing secrets before generation)
K8S_CLI=""
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    if command -v oc &> /dev/null; then
        K8S_CLI="oc"
    elif command -v kubectl &> /dev/null; then
        K8S_CLI="kubectl"
    else
        echo "Error: Neither 'oc' nor 'kubectl' found in PATH"
        exit 1
    fi
fi

# Restore existing encryption key from K8s secret (upgrade scenario)
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    # --ignore-not-found returns exit 0 with empty output when the secret
    # does not exist, and a non-zero exit code on transient errors (RBAC,
    # timeout, connectivity).
    secret_name=""
    if ! secret_name=$("$K8S_CLI" get secret "flightctl-encryption-key" -n "$NAMESPACE" \
            --ignore-not-found -o name 2>&1); then
        echo "Error: failed to look up existing encryption key secret. Aborting to avoid overwriting an existing key."
        echo "  Detail: $secret_name"
        exit 1
    fi
    if [[ -n "$secret_name" ]]; then
        echo "=== Restoring existing encryption key from K8s secret ==="
        enc_key_data=$("$K8S_CLI" get secret "flightctl-encryption-key" -n "$NAMESPACE" \
            -o jsonpath='{.data.key}')
        if [[ -z "${enc_key_data:-}" ]]; then
            echo "Error: secret flightctl-encryption-key exists but has no .data.key field. Aborting to avoid overwriting the existing secret."
            exit 1
        fi
        mkdir -p "$ENCRYPTION_DIR"
        (umask 077; echo "$enc_key_data" | base64 -d > "$ENCRYPTION_DIR/key")
        chmod 600 "$ENCRYPTION_DIR/key"
        echo "  Restored: flightctl-encryption-key"
        echo "=== Secret restoration complete ==="
        echo ""
    fi
fi

echo "=== Generating Flight Control Encryption Key in $ENCRYPTION_DIR ==="

# Generate encryption key (AES-256, base64-encoded)
ENCRYPTION_KEY_FILE="$ENCRYPTION_DIR/key"
mkdir -p "$ENCRYPTION_DIR"

if [[ -f "$ENCRYPTION_KEY_FILE" ]]; then
    # Validate existing key: must decode to exactly 32 bytes (AES-256)
    decoded_len=$(base64 -d < "$ENCRYPTION_KEY_FILE" 2>/dev/null | wc -c || true)
    if [[ "$decoded_len" -ne 32 ]]; then
        echo "Error: existing key file $ENCRYPTION_KEY_FILE is invalid (expected 32 decoded bytes, got $decoded_len). Remove it to regenerate."
        exit 1
    fi
    chmod 600 "$ENCRYPTION_KEY_FILE"
    echo "Skipped generation of encryption key (already exists)"
else
    tmp_key=$(mktemp "${ENCRYPTION_DIR}/key.XXXXXX")
    trap 'rm -f "$tmp_key"' EXIT
    openssl rand -base64 32 > "$tmp_key"
    chmod 600 "$tmp_key"
    mv "$tmp_key" "$ENCRYPTION_KEY_FILE"
    trap - EXIT
    echo "Generated encryption key (AES-256)"
fi

echo ""
echo "=== Encryption Key Generation Complete ==="
echo ""

# Create Kubernetes secrets if requested
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    echo "=== Creating Kubernetes Secrets ==="
    echo "Using CLI: $K8S_CLI"
    echo "Namespace: $NAMESPACE"
    echo ""

    echo "Creating secret: flightctl-encryption-key"
    $K8S_CLI create secret generic flightctl-encryption-key \
        --namespace="$NAMESPACE" \
        --from-file=key="$ENCRYPTION_KEY_FILE" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    # Copy encryption key secret to internal namespace if different from release namespace
    if [[ -n "$INTERNAL_NAMESPACE" ]] && [[ "$INTERNAL_NAMESPACE" != "$NAMESPACE" ]]; then
        echo ""
        echo "=== Copying encryption key secret to internal namespace: $INTERNAL_NAMESPACE ==="
        echo "Creating secret: flightctl-encryption-key"
        $K8S_CLI create secret generic flightctl-encryption-key \
            --namespace="$INTERNAL_NAMESPACE" \
            --from-file=key="$ENCRYPTION_KEY_FILE" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
        echo "Encryption key secret copied successfully to $INTERNAL_NAMESPACE"
    fi

    echo ""
    echo "=== Kubernetes Secret Creation Complete ==="
fi

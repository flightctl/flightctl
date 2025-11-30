#!/bin/bash
set -euo pipefail

# Default values
CERT_DIR=""
API_SANS=()
TELEMETRY_SANS=()
ALERTMANAGER_PROXY_SANS=()
NAMESPACE=""
CREATE_K8S_SECRETS="false"

# Parse command-line arguments
usage() {
    cat <<EOF
Usage: $0 --cert-dir <directory> [options]

Required:
  --cert-dir <dir>              Directory to store certificates

Optional:
  --api-san <dns>               DNS SAN for flightctl-api (can be specified multiple times)
  --telemetry-san <dns>         DNS SAN for telemetry-gateway (can be specified multiple times)
  --alertmanager-proxy-san <dns> DNS SAN for alertmanager-proxy (can be specified multiple times)
  --create-k8s-secrets          Create Kubernetes secrets using oc/kubectl
  --namespace <ns>              Kubernetes namespace (required if --create-k8s-secrets is set)
EOF
    exit 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --cert-dir)
            CERT_DIR="$2"
            shift 2
            ;;
        --api-san)
            API_SANS+=("$2")
            shift 2
            ;;
        --telemetry-san)
            TELEMETRY_SANS+=("$2")
            shift 2
            ;;
        --alertmanager-proxy-san)
            ALERTMANAGER_PROXY_SANS+=("$2")
            shift 2
            ;;
        --create-k8s-secrets)
            CREATE_K8S_SECRETS="true"
            shift
            ;;
        --namespace)
            NAMESPACE="$2"
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
if [[ -z "$CERT_DIR" ]]; then
    echo "Error: --cert-dir is required"
    usage
fi

if [[ "$CREATE_K8S_SECRETS" == "true" ]] && [[ -z "$NAMESPACE" ]]; then
    echo "Error: --namespace is required when --create-k8s-secrets is set"
    usage
fi

# Create certificate directory if it doesn't exist
mkdir -p "$CERT_DIR" "$CERT_DIR/flightctl-api"

# Helper function to generate SAN extension
generate_san_config() {
    local sans=("$@")
    local san_config=""

    if [[ ${#sans[@]} -gt 0 ]]; then
        # Determine if first SAN is IP or DNS
        if [[ ${sans[0]} =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            san_config="subjectAltName = IP:${sans[0]}"
        else
            san_config="subjectAltName = DNS:${sans[0]}"
        fi
        
        for ((i=1; i<${#sans[@]}; i++)); do
            if [[ ${sans[i]} =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
                san_config="${san_config}, IP:${sans[i]}"
            else
                san_config="${san_config}, DNS:${sans[i]}"
            fi
        done
    fi

    echo "$san_config"
}

echo "=== Generating Flight Control Certificates in $CERT_DIR ==="

# Step 1: Generate or use existing Flight Control CA (self-signed)
FLIGHTCTL_CA_KEY="$CERT_DIR/ca.key"
FLIGHTCTL_CA_CERT="$CERT_DIR/ca.crt"

if [[ -f "$FLIGHTCTL_CA_CERT" ]] && [[ -f "$FLIGHTCTL_CA_KEY" ]]; then
    echo "[1/5] Using existing Flight Control CA certificate"
else
    echo "[1/5] Generating self-signed Flight Control CA certificate (10 years, ECDSA P-256)"

    # Generate Flight Control CA private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$FLIGHTCTL_CA_KEY"

    # Generate self-signed Flight Control CA certificate (10 years = 3650 days)
    openssl req -new -x509 -sha256 -key "$FLIGHTCTL_CA_KEY" -out "$FLIGHTCTL_CA_CERT" \
        -days 3650 \
        -subj "/CN=flightctl-ca" \
        -addext "basicConstraints = critical, CA:TRUE" \
        -addext "keyUsage = critical, digitalSignature, keyCertSign, cRLSign"

    echo "  ✓ Flight Control CA generated: $FLIGHTCTL_CA_CERT"
fi

# Step 2: Generate Client Signer CA (intermediate CA signed by Flight Control CA)
echo "[2/5] Generating Client Signer CA - 10 years, ECDSA P-256"

CLIENT_SIGNER_CA_KEY="$CERT_DIR/flightctl-api/client-signer.key"
CLIENT_SIGNER_CA_CERT="$CERT_DIR/flightctl-api/client-signer.crt"
CLIENT_SIGNER_CA_CSR="$CERT_DIR/flightctl-api/client-signer.csr"

# Generate client-signer CA private key (ECDSA P-256)
openssl ecparam -name prime256v1 -genkey -noout -out "$CLIENT_SIGNER_CA_KEY"

# Generate CSR for client-signer CA
openssl req -new -sha256 -key "$CLIENT_SIGNER_CA_KEY" -out "$CLIENT_SIGNER_CA_CSR" \
    -subj "/CN=flightctl-client-signer-ca"

# Sign Client Signer CA with Flight Control CA (10 years = 3650 days)
openssl x509 -req -sha256 -in "$CLIENT_SIGNER_CA_CSR" \
    -CA "$FLIGHTCTL_CA_CERT" -CAkey "$FLIGHTCTL_CA_KEY" -CAcreateserial \
    -out "$CLIENT_SIGNER_CA_CERT" -days 3650 \
    -extfile <(printf "basicConstraints = critical, CA:TRUE\nkeyUsage = critical, digitalSignature, keyCertSign, cRLSign")

rm -f "$CLIENT_SIGNER_CA_CSR"
echo "  ✓ Client Signer CA generated: $CLIENT_SIGNER_CA_CERT"

# Step 3: Generate API Server TLS certificate
echo "[3/5] Generating API Server TLS certificate - 2 years, ECDSA P-256"

API_SERVER_KEY="$CERT_DIR/flightctl-api/server.key"
API_SERVER_CERT="$CERT_DIR/flightctl-api/server.crt"
API_SERVER_CSR="$CERT_DIR/flightctl-api/server.csr"

# Generate API server private key (ECDSA P-256)
openssl ecparam -name prime256v1 -genkey -noout -out "$API_SERVER_KEY"

# Generate CSR for API server
openssl req -new -sha256 -key "$API_SERVER_KEY" -out "$API_SERVER_CSR" \
    -subj "/CN=flightctl-api"

# Build SAN configuration
if [[ ${#API_SANS[@]} -gt 0 ]]; then
    API_SAN_CONFIG=$(generate_san_config "${API_SANS[@]}")
    echo "  API Server SANs: ${API_SANS[*]}"
else
    API_SAN_CONFIG=""
    echo "  Warning: No SANs specified for API server certificate"
fi

# Sign API server certificate with flightctl-ca (2 years = 730 days)
EXT_CONFIG="basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth"

if [[ -n "$API_SAN_CONFIG" ]]; then
    EXT_CONFIG="${EXT_CONFIG}
${API_SAN_CONFIG}"
fi

openssl x509 -req -sha256 -in "$API_SERVER_CSR" \
    -CA "$FLIGHTCTL_CA_CERT" -CAkey "$FLIGHTCTL_CA_KEY" -CAcreateserial \
    -out "$API_SERVER_CERT" -days 730 \
    -extfile <(printf "%s" "$EXT_CONFIG")

rm -f "$API_SERVER_CSR"
echo "  ✓ API Server TLS certificate generated: $API_SERVER_CERT"

# Step 4: Generate Telemetry Gateway TLS certificate
if [[ ${#TELEMETRY_SANS[@]} -gt 0 ]]; then
    echo "[4/5] Generating Telemetry Gateway TLS certificate - 2 years, ECDSA P-256"
    
    # Create telemetry gateway directory
    mkdir -p "$CERT_DIR/flightctl-telemetry-gateway"

    TELEMETRY_KEY="$CERT_DIR/flightctl-telemetry-gateway/server.key"
    TELEMETRY_CERT="$CERT_DIR/flightctl-telemetry-gateway/server.crt"
    TELEMETRY_CSR="$CERT_DIR/flightctl-telemetry-gateway/server.csr"

    # Generate telemetry gateway private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$TELEMETRY_KEY"

    # Generate CSR for telemetry gateway
    openssl req -new -sha256 -key "$TELEMETRY_KEY" -out "$TELEMETRY_CSR" \
        -subj "/CN=flightctl-telemetry-gateway"

    # Build SAN configuration
    TELEMETRY_SAN_CONFIG=$(generate_san_config "${TELEMETRY_SANS[@]}")
    echo "  Telemetry Gateway SANs: ${TELEMETRY_SANS[*]}"

    # Sign telemetry gateway certificate with flightctl-ca (2 years = 730 days)
    EXT_CONFIG="basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
${TELEMETRY_SAN_CONFIG}"

    openssl x509 -req -sha256 -in "$TELEMETRY_CSR" \
        -CA "$FLIGHTCTL_CA_CERT" -CAkey "$FLIGHTCTL_CA_KEY" -CAcreateserial \
        -out "$TELEMETRY_CERT" -days 730 \
        -extfile <(printf "%s" "$EXT_CONFIG")

    rm -f "$TELEMETRY_CSR"
    echo "  ✓ Telemetry Gateway TLS certificate generated: $TELEMETRY_CERT"
else
    echo "[4/5] Skipping Telemetry Gateway TLS certificate (no SANs specified)"
fi

# Step 5: Generate Alertmanager Proxy TLS certificate
if [[ ${#ALERTMANAGER_PROXY_SANS[@]} -gt 0 ]]; then
    echo "[5/6] Generating Alertmanager Proxy TLS certificate - 2 years, ECDSA P-256"
    
    # Create alertmanager proxy directory
    mkdir -p "$CERT_DIR/flightctl-alertmanager-proxy"

    ALERTMANAGER_PROXY_KEY="$CERT_DIR/flightctl-alertmanager-proxy/server.key"
    ALERTMANAGER_PROXY_CERT="$CERT_DIR/flightctl-alertmanager-proxy/server.crt"
    ALERTMANAGER_PROXY_CSR="$CERT_DIR/flightctl-alertmanager-proxy/server.csr"

    # Generate alertmanager proxy private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$ALERTMANAGER_PROXY_KEY"

    # Generate CSR for alertmanager proxy
    openssl req -new -sha256 -key "$ALERTMANAGER_PROXY_KEY" -out "$ALERTMANAGER_PROXY_CSR" \
        -subj "/CN=flightctl-alertmanager-proxy"

    # Build SAN configuration
    ALERTMANAGER_PROXY_SAN_CONFIG=$(generate_san_config "${ALERTMANAGER_PROXY_SANS[@]}")
    echo "  Alertmanager Proxy SANs: ${ALERTMANAGER_PROXY_SANS[*]}"

    # Sign alertmanager proxy certificate with flightctl-ca (2 years = 730 days)
    EXT_CONFIG="basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
${ALERTMANAGER_PROXY_SAN_CONFIG}"

    openssl x509 -req -sha256 -in "$ALERTMANAGER_PROXY_CSR" \
        -CA "$FLIGHTCTL_CA_CERT" -CAkey "$FLIGHTCTL_CA_KEY" -CAcreateserial \
        -out "$ALERTMANAGER_PROXY_CERT" -days 730 \
        -extfile <(printf "%s" "$EXT_CONFIG")

    rm -f "$ALERTMANAGER_PROXY_CSR"
    echo "  ✓ Alertmanager Proxy TLS certificate generated: $ALERTMANAGER_PROXY_CERT"
else
    echo "[5/6] Skipping Alertmanager Proxy TLS certificate (no SANs specified)"
fi

# Step 5: Generate CA Bundle (contains both flightctl-ca and client-signer-ca)
echo "[6/6] Generating CA Bundle (contains both flightctl-ca and client-signer-ca)"
CA_BUNDLE="$CERT_DIR/ca-bundle.crt"
cat "$FLIGHTCTL_CA_CERT" "$CLIENT_SIGNER_CA_CERT" > "$CA_BUNDLE"
echo "  ✓ CA Bundle created: $CA_BUNDLE"

# Clean up serial files
rm -f "$CERT_DIR"/*.srl

echo ""
echo "=== Certificate Generation Complete ==="
echo ""

# Create Kubernetes secrets if requested
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    echo "=== Creating Kubernetes Secrets ==="

    # Determine which CLI to use (oc or kubectl)
    if command -v oc &> /dev/null; then
        K8S_CLI="oc"
    elif command -v kubectl &> /dev/null; then
        K8S_CLI="kubectl"
    else
        echo "Error: Neither 'oc' nor 'kubectl' found in PATH"
        exit 1
    fi

    echo "Using CLI: $K8S_CLI"
    echo "Namespace: $NAMESPACE"
    echo ""

    # Create flightctl-ca secret
    echo "Creating secret: flightctl-ca"
    $K8S_CLI create secret tls flightctl-ca \
        --namespace="$NAMESPACE" \
        --cert="$FLIGHTCTL_CA_CERT" \
        --key="$FLIGHTCTL_CA_KEY" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    # Create flightctl-client-signer-ca secret
    echo "Creating secret: flightctl-client-signer-ca"
    $K8S_CLI create secret tls flightctl-client-signer-ca \
        --namespace="$NAMESPACE" \
        --cert="$CLIENT_SIGNER_CA_CERT" \
        --key="$CLIENT_SIGNER_CA_KEY" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    # Create flightctl-api-server-tls secret
    echo "Creating secret: flightctl-api-server-tls"
    $K8S_CLI create secret tls flightctl-api-server-tls \
        --namespace="$NAMESPACE" \
        --cert="$API_SERVER_CERT" \
        --key="$API_SERVER_KEY" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    # Create flightctl-telemetry-gateway-server-tls secret (only if certificate was generated)
    if [[ ${#TELEMETRY_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-telemetry-gateway-server-tls"
        $K8S_CLI create secret tls flightctl-telemetry-gateway-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$TELEMETRY_CERT" \
            --key="$TELEMETRY_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create flightctl-alertmanager-proxy-server-tls secret (only if certificate was generated)
    if [[ ${#ALERTMANAGER_PROXY_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-alertmanager-proxy-server-tls"
        $K8S_CLI create secret tls flightctl-alertmanager-proxy-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$ALERTMANAGER_PROXY_CERT" \
            --key="$ALERTMANAGER_PROXY_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create CA bundle Secret (compatible with trust-manager format)
    echo "Creating Secret: flightctl-ca-bundle"
    $K8S_CLI create secret generic flightctl-ca-bundle \
        --namespace="$NAMESPACE" \
        --from-file=ca-bundle.crt="$CA_BUNDLE" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    echo ""
    echo "=== Kubernetes Secret Creation Complete ==="
fi

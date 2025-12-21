#!/bin/bash
set -euo pipefail

# Default values
CERT_DIR=""
API_SANS=()
TELEMETRY_SANS=()
ALERTMANAGER_PROXY_SANS=()
PAM_ISSUER_SANS=()
UI_SANS=()
CLI_ARTIFACTS_SANS=()
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
  --pam-issuer-san <dns>        DNS SAN for flightctl-pam-issuer (can be specified multiple times)
  --ui-san <dns>                DNS SAN for flightctl-ui (can be specified multiple times)
  --cli-artifacts-san <dns>     DNS SAN for flightctl-cli-artifacts (can be specified multiple times)
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
        --pam-issuer-san)
            PAM_ISSUER_SANS+=("$2")
            shift 2
            ;;
        --ui-san)
            UI_SANS+=("$2")
            shift 2
            ;;
        --cli-artifacts-san)
            CLI_ARTIFACTS_SANS+=("$2")
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

# Helper function to generate self-signed root CA certificate
generate_root_ca() {
    local cn="$1"
    local key_path="$2"
    local cert_path="$3"

    # Generate private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$key_path"
    chmod 600 "$key_path"

    # Generate self-signed certificate (10 years = 3650 days)
    openssl req -new -x509 -sha256 -key "$key_path" -out "$cert_path" \
        -days 3650 \
        -subj "/CN=$cn" \
        -addext "basicConstraints = critical, CA:TRUE" \
        -addext "keyUsage = critical, digitalSignature, keyCertSign, cRLSign"
}

# Helper function to generate intermediate CA certificate
generate_intermediate_ca() {
    local cn="$1"
    local key_path="$2"
    local cert_path="$3"
    local ca_cert="$4"
    local ca_key="$5"

    local csr_path="${cert_path%.crt}.csr"

    # Generate private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$key_path"
    chmod 600 "$key_path"

    # Generate CSR
    openssl req -new -sha256 -key "$key_path" -out "$csr_path" -subj "/CN=$cn"

    # Sign with CA (10 years = 3650 days)
    openssl x509 -req -sha256 -in "$csr_path" \
        -CA "$ca_cert" -CAkey "$ca_key" -CAcreateserial \
        -out "$cert_path" -days 3650 \
        -extfile <(printf "basicConstraints = critical, CA:TRUE\nkeyUsage = critical, digitalSignature, keyCertSign, cRLSign")

    rm -f "$csr_path"
}

# Helper function to generate server TLS certificate
generate_server_cert() {
    local cn="$1"
    local key_path="$2"
    local cert_path="$3"
    local ca_cert="$4"
    local ca_key="$5"
    shift 5
    local sans=("$@")

    local csr_path="${cert_path%.crt}.csr"

    # Generate private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$key_path"
    chmod 600 "$key_path"

    # Generate CSR
    openssl req -new -sha256 -key "$key_path" -out "$csr_path" -subj "/CN=$cn"

    # Build extension config
    local ext_config="basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth"

    if [[ ${#sans[@]} -gt 0 ]]; then
        local san_config=$(generate_san_config "${sans[@]}")
        ext_config="${ext_config}
${san_config}"
    fi

    # Sign with CA (2 years = 730 days)
    openssl x509 -req -sha256 -in "$csr_path" \
        -CA "$ca_cert" -CAkey "$ca_key" -CAcreateserial \
        -out "$cert_path" -days 730 \
        -extfile <(printf "%s" "$ext_config")

    rm -f "$csr_path"
}

echo "=== Generating Flight Control Certificates in $CERT_DIR ==="

# Create certificate directory if it doesn't exist

# Step 1: Flight Control CA (self-signed root certificate)
mkdir -p "$CERT_DIR"

FLIGHTCTL_CA_KEY="$CERT_DIR/ca.key"
FLIGHTCTL_CA_CERT="$CERT_DIR/ca.crt"

if [[ -f "$FLIGHTCTL_CA_CERT" ]] && [[ -f "$FLIGHTCTL_CA_KEY" ]]; then
    echo "[1/10] Skipped generation of Flight Control CA certificate (already exists)"
else
    generate_root_ca "flightctl-ca" "$FLIGHTCTL_CA_KEY" "$FLIGHTCTL_CA_CERT"
    echo "[1/10] Generated Flight Control CA certificate (10 years, ECDSA P-256)"
fi

# Step 2: Client Signer CA (intermediate CA signed by Flight Control CA)
mkdir -p "$CERT_DIR/flightctl-api"

CLIENT_SIGNER_CA_KEY="$CERT_DIR/flightctl-api/client-signer.key"
CLIENT_SIGNER_CA_CERT="$CERT_DIR/flightctl-api/client-signer.crt"

if [[ -f "$CLIENT_SIGNER_CA_CERT" ]] && [[ -f "$CLIENT_SIGNER_CA_KEY" ]]; then
    echo "[2/10] Skipped generation of Client Signer CA certificate (already exists)"
else
    generate_intermediate_ca "flightctl-client-signer-ca" \
        "$CLIENT_SIGNER_CA_KEY" "$CLIENT_SIGNER_CA_CERT" \
        "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY"
    echo "[2/10] Generated Client Signer CA certificate (10 years, ECDSA P-256)"
fi

# Step 3: PAM Issuer Token Signer CA (intermediate CA signed by Flight Control CA)
if [[ ${#PAM_ISSUER_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-pam-issuer"

    PAM_ISSUER_TOKEN_SIGNER_CA_KEY="$CERT_DIR/flightctl-pam-issuer/token-signer.key"
    PAM_ISSUER_TOKEN_SIGNER_CA_CERT="$CERT_DIR/flightctl-pam-issuer/token-signer.crt"

    if [[ -f "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" ]] && [[ -f "$PAM_ISSUER_TOKEN_SIGNER_CA_KEY" ]]; then
        echo "[3/10] Skipped generation of PAM Issuer Token Signer CA certificate (already exists)"
    else
        generate_intermediate_ca "flightctl-pam-issuer-token-signer-ca" \
            "$PAM_ISSUER_TOKEN_SIGNER_CA_KEY" "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY"
        echo "[3/10] Generated PAM Issuer Token Signer CA certificate (10 years, ECDSA P-256)"
    fi
else
    echo "[3/10] Skipped generation of PAM Issuer Token Signer CA certificate (no PAM Issuer SANs specified)"
fi

# Step 4: API Server TLS certificate
mkdir -p "$CERT_DIR/flightctl-api"

API_SERVER_KEY="$CERT_DIR/flightctl-api/server.key"
API_SERVER_CERT="$CERT_DIR/flightctl-api/server.crt"

if [[ -f "$API_SERVER_CERT" ]] && [[ -f "$API_SERVER_KEY" ]]; then
    echo "[4/10] Skipped generation of API Server TLS certificate (already exists)"
else
    generate_server_cert "flightctl-api" \
        "$API_SERVER_KEY" "$API_SERVER_CERT" \
        "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
        "${API_SANS[@]}"
    echo "[4/10] Generated API Server TLS certificate (2 years, ECDSA P-256)"
fi

# Step 5: PAM Issuer Server TLS certificate
if [[ ${#PAM_ISSUER_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-pam-issuer"

    PAM_ISSUER_SERVER_KEY="$CERT_DIR/flightctl-pam-issuer/server.key"
    PAM_ISSUER_SERVER_CERT="$CERT_DIR/flightctl-pam-issuer/server.crt"

    if [[ -f "$PAM_ISSUER_SERVER_CERT" ]] && [[ -f "$PAM_ISSUER_SERVER_KEY" ]]; then
        echo "[5/10] Skipped generation of PAM Issuer Server TLS certificate (already exists)"
    else
        generate_server_cert "flightctl-pam-issuer" \
            "$PAM_ISSUER_SERVER_KEY" "$PAM_ISSUER_SERVER_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${PAM_ISSUER_SANS[@]}"
        echo "[5/10] Generated PAM Issuer Server TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "[5/10] Skipped generation of PAM Issuer Server TLS certificate (no PAM Issuer SANs specified)"
fi

# Step 6: Telemetry Gateway TLS certificate
if [[ ${#TELEMETRY_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-telemetry-gateway"

    TELEMETRY_KEY="$CERT_DIR/flightctl-telemetry-gateway/server.key"
    TELEMETRY_CERT="$CERT_DIR/flightctl-telemetry-gateway/server.crt"

    if [[ -f "$TELEMETRY_CERT" ]] && [[ -f "$TELEMETRY_KEY" ]]; then
        echo "[6/10] Skipped generation of Telemetry Gateway TLS certificate (already exists)"
    else
        generate_server_cert "flightctl-telemetry-gateway" \
            "$TELEMETRY_KEY" "$TELEMETRY_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${TELEMETRY_SANS[@]}"
        echo "[6/10] Generated Telemetry Gateway TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "[6/10] Skipped generation of Telemetry Gateway TLS certificate (no SANs specified)"
fi

# Step 7: Alertmanager Proxy TLS certificate
if [[ ${#ALERTMANAGER_PROXY_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-alertmanager-proxy"

    ALERTMANAGER_PROXY_KEY="$CERT_DIR/flightctl-alertmanager-proxy/server.key"
    ALERTMANAGER_PROXY_CERT="$CERT_DIR/flightctl-alertmanager-proxy/server.crt"

    if [[ -f "$ALERTMANAGER_PROXY_CERT" ]] && [[ -f "$ALERTMANAGER_PROXY_KEY" ]]; then
        echo "[7/10] Skipped generation of Alertmanager Proxy TLS certificate (already exists)"
    else
        generate_server_cert "flightctl-alertmanager-proxy" \
            "$ALERTMANAGER_PROXY_KEY" "$ALERTMANAGER_PROXY_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${ALERTMANAGER_PROXY_SANS[@]}"
        echo "[7/10] Generated Alertmanager Proxy TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "[7/10] Skipped generation of Alertmanager Proxy TLS certificate (no SANs specified)"
fi

# Step 8: UI TLS certificate
if [[ ${#UI_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-ui"

    UI_KEY="$CERT_DIR/flightctl-ui/server.key"
    UI_CERT="$CERT_DIR/flightctl-ui/server.crt"

    if [[ -f "$UI_CERT" ]] && [[ -f "$UI_KEY" ]]; then
        echo "[8/10] Skipped generation of UI TLS certificate (already exists)"
    else
        generate_server_cert "flightctl-ui" \
            "$UI_KEY" "$UI_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${UI_SANS[@]}"
        echo "[8/10] Generated UI TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "[8/10] Skipped generation of UI TLS certificate (no SANs specified)"
fi

# Step 9: CLI Artifacts TLS certificate
if [[ ${#CLI_ARTIFACTS_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-cli-artifacts"

    CLI_ARTIFACTS_KEY="$CERT_DIR/flightctl-cli-artifacts/server.key"
    CLI_ARTIFACTS_CERT="$CERT_DIR/flightctl-cli-artifacts/server.crt"

    if [[ -f "$CLI_ARTIFACTS_CERT" ]] && [[ -f "$CLI_ARTIFACTS_KEY" ]]; then
        echo "[9/10] Skipped generation of CLI Artifacts TLS certificate (already exists)"
    else
        generate_server_cert "flightctl-cli-artifacts" \
            "$CLI_ARTIFACTS_KEY" "$CLI_ARTIFACTS_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${CLI_ARTIFACTS_SANS[@]}"
        echo "[9/10] Generated CLI Artifacts TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "[9/10] Skipped generation of CLI Artifacts TLS certificate (no SANs specified)"
fi

# Step 10: CA Bundle
CA_BUNDLE="$CERT_DIR/ca-bundle.crt"

if [[ ${#PAM_ISSUER_SANS[@]} -gt 0 ]]; then
    cat "$FLIGHTCTL_CA_CERT" "$CLIENT_SIGNER_CA_CERT" "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" > "$CA_BUNDLE"
else
    cat "$FLIGHTCTL_CA_CERT" "$CLIENT_SIGNER_CA_CERT" > "$CA_BUNDLE"
fi
echo "[10/10] Generated CA Bundle"

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

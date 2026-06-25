#!/bin/bash
set -euo pipefail

# Default values
CERT_DIR=""
GATEWAY_SANS=()
API_SANS=()
TELEMETRY_SANS=()
ALERTMANAGER_PROXY_SANS=()
PAM_ISSUER_SANS=()
UI_SANS=()
CLI_ARTIFACTS_SANS=()
IMAGEBUILDER_API_SANS=()
PROMETHEUS_SANS=()
GRAFANA_SANS=()
USERINFO_PROXY_SANS=()
REMOTE_ACCESS_SANS=()
NAMESPACE=""
INTERNAL_NAMESPACE=""
CREATE_K8S_SECRETS="false"

# Parse command-line arguments
usage() {
    cat <<EOF
Usage: $0 --cert-dir <directory> [options]

Required:
  --cert-dir <dir>              Directory to store certificates

Optional:
  --gateway-san <dns>           DNS SAN for flightctl-gateway (can be specified multiple times)
  --api-san <dns>               DNS SAN for flightctl-api (can be specified multiple times)
  --telemetry-san <dns>         DNS SAN for telemetry-gateway (can be specified multiple times)
  --alertmanager-proxy-san <dns> DNS SAN for alertmanager-proxy (can be specified multiple times)
  --pam-issuer-san <dns>        DNS SAN for flightctl-pam-issuer (can be specified multiple times)
  --ui-san <dns>                DNS SAN for flightctl-ui (can be specified multiple times)
  --cli-artifacts-san <dns>     DNS SAN for flightctl-cli-artifacts (can be specified multiple times)
  --imagebuilder-api-san <dns>  DNS SAN for flightctl-imagebuilder-api (can be specified multiple times)
  --prometheus-san <dns>        DNS SAN for flightctl-prometheus (can be specified multiple times)
  --grafana-san <dns>           DNS SAN for flightctl-grafana (can be specified multiple times)
  --userinfo-proxy-san <dns>    DNS SAN for flightctl-userinfo-proxy (can be specified multiple times)
  --remote-access-san <dns>     DNS SAN for flightctl-remote-access (can be specified multiple times)
  --create-k8s-secrets          Create Kubernetes secrets using oc/kubectl
  --namespace <ns>              Kubernetes namespace (required if --create-k8s-secrets is set)
  --internal-namespace <ns>      Internal namespace to copy CA secrets to (optional)
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
        --gateway-san)
            GATEWAY_SANS+=("$2")
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
        --imagebuilder-api-san)
            IMAGEBUILDER_API_SANS+=("$2")
            shift 2
            ;;
        --prometheus-san)
            PROMETHEUS_SANS+=("$2")
            shift 2
            ;;
        --grafana-san)
            GRAFANA_SANS+=("$2")
            shift 2
            ;;
        --userinfo-proxy-san)
            USERINFO_PROXY_SANS+=("$2")
            shift 2
            ;;
        --remote-access-san)
            REMOTE_ACCESS_SANS+=("$2")
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
        --internal-namespace)
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
if [[ -z "$CERT_DIR" ]]; then
    echo "Error: --cert-dir is required"
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

# Helper function to restore a TLS secret from K8s into cert/key files on disk
restore_tls_secret() {
    local secret_name="$1"
    local cert_path="$2"
    local key_path="$3"

    # Check if the secret exists; skip silently if not (fresh install)
    if ! $K8S_CLI get secret "$secret_name" -n "$NAMESPACE" &>/dev/null; then
        return 0
    fi

    # Secret exists — fetch without suppressing errors so transient
    # failures (network, RBAC) surface instead of causing CA regeneration
    local tls_crt tls_key
    tls_crt=$($K8S_CLI get secret "$secret_name" -n "$NAMESPACE" \
        -o jsonpath='{.data.tls\.crt}')
    tls_key=$($K8S_CLI get secret "$secret_name" -n "$NAMESPACE" \
        -o jsonpath='{.data.tls\.key}')

    if [[ -n "$tls_crt" ]] && [[ -n "$tls_key" ]]; then
        mkdir -p "$(dirname "$cert_path")"
        echo "$tls_crt" | base64 -d > "$cert_path"
        echo "$tls_key" | base64 -d > "$key_path"
        chmod 600 "$key_path"
        echo "  Restored: $secret_name"
    fi
}

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

# Helper function to check if a certificate has all expected SANs
# Arguments: cert_path san1 san2 san3 ...
# Returns 0 if all SANs match, 1 otherwise
cert_has_expected_sans() {
    local cert_path="$1"
    shift
    local expected_sans=("$@")

    if [[ ! -f "$cert_path" ]]; then
        return 1
    fi

    for san in "${expected_sans[@]}"; do
        if [[ "$san" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            if ! openssl x509 -in "$cert_path" -noout -checkip "$san" >/dev/null 2>&1; then
                echo "  Certificate $cert_path missing expected SAN: $san - will regenerate"
                return 1
            fi
            continue
        fi

        if ! openssl x509 -in "$cert_path" -noout -checkhost "$san" >/dev/null 2>&1; then
            echo "  Certificate $cert_path missing expected SAN: $san - will regenerate"
            return 1
        fi
    done

    return 0
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
    chmod 640 "$key_path"

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

# Restore existing certificates from K8s secrets so that
# the "skip if exists" checks below preserve them on upgrade
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    echo "=== Restoring existing certificates from K8s secrets ==="
    mkdir -p "$CERT_DIR"

    restore_tls_secret "flightctl-ca" \
        "$CERT_DIR/ca.crt" "$CERT_DIR/ca.key"
    restore_tls_secret "flightctl-client-signer-ca" \
        "$CERT_DIR/flightctl-api/client-signer.crt" "$CERT_DIR/flightctl-api/client-signer.key"
    restore_tls_secret "flightctl-api-server-tls" \
        "$CERT_DIR/flightctl-api/server.crt" "$CERT_DIR/flightctl-api/server.key"
    restore_tls_secret "flightctl-telemetry-gateway-server-tls" \
        "$CERT_DIR/flightctl-telemetry-gateway/server.crt" "$CERT_DIR/flightctl-telemetry-gateway/server.key"
    restore_tls_secret "flightctl-alertmanager-proxy-server-tls" \
        "$CERT_DIR/flightctl-alertmanager-proxy/server.crt" "$CERT_DIR/flightctl-alertmanager-proxy/server.key"
    restore_tls_secret "flightctl-imagebuilder-api-server-tls" \
        "$CERT_DIR/flightctl-imagebuilder-api/server.crt" "$CERT_DIR/flightctl-imagebuilder-api/server.key"
    restore_tls_secret "flightctl-ui-server-tls" \
        "$CERT_DIR/flightctl-ui/server.crt" "$CERT_DIR/flightctl-ui/server.key"
    restore_tls_secret "flightctl-cli-artifacts-server-tls" \
        "$CERT_DIR/flightctl-cli-artifacts/server.crt" "$CERT_DIR/flightctl-cli-artifacts/server.key"
    restore_tls_secret "flightctl-remote-access-server-tls" \
        "$CERT_DIR/flightctl-remote-access/server.crt" "$CERT_DIR/flightctl-remote-access/server.key"

    # Restore CA bundle (generic secret, not TLS)
    if $K8S_CLI get secret "flightctl-ca-bundle" -n "$NAMESPACE" &>/dev/null; then
        ca_bundle_data=$($K8S_CLI get secret "flightctl-ca-bundle" -n "$NAMESPACE" \
            -o jsonpath='{.data.ca-bundle\.crt}')
        if [[ -n "${ca_bundle_data:-}" ]]; then
            echo "$ca_bundle_data" | base64 -d > "$CERT_DIR/ca-bundle.crt"
            echo "  Restored: flightctl-ca-bundle"
        fi
    fi

    echo "=== Secret restoration complete ==="
    echo ""
fi

echo "=== Generating Flight Control Certificates in $CERT_DIR ==="

# Create certificate directory if it doesn't exist

# Flight Control CA (self-signed root certificate)
mkdir -p "$CERT_DIR"

FLIGHTCTL_CA_KEY="$CERT_DIR/ca.key"
FLIGHTCTL_CA_CERT="$CERT_DIR/ca.crt"

if [[ -f "$FLIGHTCTL_CA_CERT" ]] && [[ -f "$FLIGHTCTL_CA_KEY" ]]; then
    echo "Skipped generation of Flight Control CA certificate (already exists)"
else
    generate_root_ca "flightctl-ca" "$FLIGHTCTL_CA_KEY" "$FLIGHTCTL_CA_CERT"
    echo "Generated Flight Control CA certificate (10 years, ECDSA P-256)"
fi

# Client Signer CA (intermediate CA signed by Flight Control CA)
mkdir -p "$CERT_DIR/flightctl-api"

CLIENT_SIGNER_CA_KEY="$CERT_DIR/flightctl-api/client-signer.key"
CLIENT_SIGNER_CA_CERT="$CERT_DIR/flightctl-api/client-signer.crt"

if [[ -f "$CLIENT_SIGNER_CA_CERT" ]] && [[ -f "$CLIENT_SIGNER_CA_KEY" ]]; then
    echo "Skipped generation of Client Signer CA certificate (already exists)"
else
    generate_intermediate_ca "flightctl-client-signer-ca" \
        "$CLIENT_SIGNER_CA_KEY" "$CLIENT_SIGNER_CA_CERT" \
        "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY"
    echo "Generated Client Signer CA certificate (10 years, ECDSA P-256)"
fi

# PAM Issuer Token Signer CA (intermediate CA signed by Flight Control CA)
mkdir -p "$CERT_DIR/flightctl-pam-issuer"

PAM_ISSUER_TOKEN_SIGNER_CA_KEY="$CERT_DIR/flightctl-pam-issuer/token-signer.key"
PAM_ISSUER_TOKEN_SIGNER_CA_CERT="$CERT_DIR/flightctl-pam-issuer/token-signer.crt"

if [[ -f "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" ]] && [[ -f "$PAM_ISSUER_TOKEN_SIGNER_CA_KEY" ]]; then
    echo "Skipped generation of PAM Issuer Token Signer CA certificate (already exists)"
else
    generate_intermediate_ca "flightctl-pam-issuer-token-signer-ca" \
        "$PAM_ISSUER_TOKEN_SIGNER_CA_KEY" "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" \
        "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY"
    echo "Generated PAM Issuer Token Signer CA certificate (10 years, ECDSA P-256)"
fi

if [[ ${#GATEWAY_SANS[@]} -gt 0 ]]; then
  mkdir -p "$CERT_DIR/flightctl-gateway"

  GATEWAY_SERVER_KEY="$CERT_DIR/flightctl-gateway/server.key"
  GATEWAY_SERVER_CERT="$CERT_DIR/flightctl-gateway/server.crt"

  if [[ -f "$GATEWAY_SERVER_CERT" ]] && [[ -f "$GATEWAY_SERVER_KEY" ]] && cert_has_expected_sans "$GATEWAY_SERVER_CERT" "${GATEWAY_SANS[@]}"; then
      echo "Skipped generation of Gateway Server TLS certificate (already exists with correct SANs)"
  else
      generate_server_cert "flightctl-gateway" \
          "$GATEWAY_SERVER_KEY" "$GATEWAY_SERVER_CERT" \
          "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
          "${GATEWAY_SANS[@]}"
      echo "Generated Gateway Server TLS certificate (2 years, ECDSA P-256)"
  fi
else
  echo "Skipped generation of Gateway Server TLS certificate (no Gateway SANs specified)"
fi

# API Server TLS certificate
mkdir -p "$CERT_DIR/flightctl-api"

API_SERVER_KEY="$CERT_DIR/flightctl-api/server.key"
API_SERVER_CERT="$CERT_DIR/flightctl-api/server.crt"

if [[ -f "$API_SERVER_CERT" ]] && [[ -f "$API_SERVER_KEY" ]] && cert_has_expected_sans "$API_SERVER_CERT" "${API_SANS[@]}"; then
    echo "Skipped generation of API Server TLS certificate (already exists with correct SANs)"
else
    generate_server_cert "flightctl-api" \
        "$API_SERVER_KEY" "$API_SERVER_CERT" \
        "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
        "${API_SANS[@]}"
    echo "Generated API Server TLS certificate (2 years, ECDSA P-256)"
fi

# PAM Issuer Server TLS certificate
if [[ ${#PAM_ISSUER_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-pam-issuer"

    PAM_ISSUER_SERVER_KEY="$CERT_DIR/flightctl-pam-issuer/server.key"
    PAM_ISSUER_SERVER_CERT="$CERT_DIR/flightctl-pam-issuer/server.crt"

    if [[ -f "$PAM_ISSUER_SERVER_CERT" ]] && [[ -f "$PAM_ISSUER_SERVER_KEY" ]] && cert_has_expected_sans "$PAM_ISSUER_SERVER_CERT" "${PAM_ISSUER_SANS[@]}"; then
        echo "Skipped generation of PAM Issuer Server TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-pam-issuer" \
            "$PAM_ISSUER_SERVER_KEY" "$PAM_ISSUER_SERVER_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${PAM_ISSUER_SANS[@]}"
        echo "Generated PAM Issuer Server TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of PAM Issuer Server TLS certificate (no PAM Issuer SANs specified)"
fi

# Telemetry Gateway TLS certificate
if [[ ${#TELEMETRY_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-telemetry-gateway"

    TELEMETRY_KEY="$CERT_DIR/flightctl-telemetry-gateway/server.key"
    TELEMETRY_CERT="$CERT_DIR/flightctl-telemetry-gateway/server.crt"

    if [[ -f "$TELEMETRY_CERT" ]] && [[ -f "$TELEMETRY_KEY" ]] && cert_has_expected_sans "$TELEMETRY_CERT" "${TELEMETRY_SANS[@]}"; then
        echo "Skipped generation of Telemetry Gateway TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-telemetry-gateway" \
            "$TELEMETRY_KEY" "$TELEMETRY_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${TELEMETRY_SANS[@]}"
        echo "Generated Telemetry Gateway TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of Telemetry Gateway TLS certificate (no SANs specified)"
fi

# Alertmanager Proxy TLS certificate
if [[ ${#ALERTMANAGER_PROXY_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-alertmanager-proxy"

    ALERTMANAGER_PROXY_KEY="$CERT_DIR/flightctl-alertmanager-proxy/server.key"
    ALERTMANAGER_PROXY_CERT="$CERT_DIR/flightctl-alertmanager-proxy/server.crt"

    if [[ -f "$ALERTMANAGER_PROXY_CERT" ]] && [[ -f "$ALERTMANAGER_PROXY_KEY" ]] && cert_has_expected_sans "$ALERTMANAGER_PROXY_CERT" "${ALERTMANAGER_PROXY_SANS[@]}"; then
        echo "Skipped generation of Alertmanager Proxy TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-alertmanager-proxy" \
            "$ALERTMANAGER_PROXY_KEY" "$ALERTMANAGER_PROXY_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${ALERTMANAGER_PROXY_SANS[@]}"
        echo "Generated Alertmanager Proxy TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of Alertmanager Proxy TLS certificate (no SANs specified)"
fi

# UI TLS certificate
if [[ ${#UI_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-ui"

    UI_KEY="$CERT_DIR/flightctl-ui/server.key"
    UI_CERT="$CERT_DIR/flightctl-ui/server.crt"

    if [[ -f "$UI_CERT" ]] && [[ -f "$UI_KEY" ]] && cert_has_expected_sans "$UI_CERT" "${UI_SANS[@]}"; then
        echo "Skipped generation of UI TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-ui" \
            "$UI_KEY" "$UI_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${UI_SANS[@]}"
        echo "Generated UI TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of UI TLS certificate (no SANs specified)"
fi

# CLI Artifacts TLS certificate
if [[ ${#CLI_ARTIFACTS_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-cli-artifacts"

    CLI_ARTIFACTS_KEY="$CERT_DIR/flightctl-cli-artifacts/server.key"
    CLI_ARTIFACTS_CERT="$CERT_DIR/flightctl-cli-artifacts/server.crt"

    if [[ -f "$CLI_ARTIFACTS_CERT" ]] && [[ -f "$CLI_ARTIFACTS_KEY" ]] && cert_has_expected_sans "$CLI_ARTIFACTS_CERT" "${CLI_ARTIFACTS_SANS[@]}"; then
        echo "Skipped generation of CLI Artifacts TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-cli-artifacts" \
            "$CLI_ARTIFACTS_KEY" "$CLI_ARTIFACTS_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${CLI_ARTIFACTS_SANS[@]}"
        echo "Generated CLI Artifacts TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of CLI Artifacts TLS certificate (no SANs specified)"
fi

# ImageBuilder API TLS certificate
if [[ ${#IMAGEBUILDER_API_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-imagebuilder-api"

    IMAGEBUILDER_API_KEY="$CERT_DIR/flightctl-imagebuilder-api/server.key"
    IMAGEBUILDER_API_CERT="$CERT_DIR/flightctl-imagebuilder-api/server.crt"

    if [[ -f "$IMAGEBUILDER_API_CERT" ]] && [[ -f "$IMAGEBUILDER_API_KEY" ]] && cert_has_expected_sans "$IMAGEBUILDER_API_CERT" "${IMAGEBUILDER_API_SANS[@]}"; then
        echo "Skipped generation of ImageBuilder API TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-imagebuilder-api" \
            "$IMAGEBUILDER_API_KEY" "$IMAGEBUILDER_API_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${IMAGEBUILDER_API_SANS[@]}"
        echo "Generated ImageBuilder API TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of ImageBuilder API TLS certificate (no SANs specified)"
fi

# Prometheus TLS certificate
if [[ ${#PROMETHEUS_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-prometheus"

    PROMETHEUS_KEY="$CERT_DIR/flightctl-prometheus/server.key"
    PROMETHEUS_CERT="$CERT_DIR/flightctl-prometheus/server.crt"

    if [[ -f "$PROMETHEUS_CERT" ]] && [[ -f "$PROMETHEUS_KEY" ]] && cert_has_expected_sans "$PROMETHEUS_CERT" "${PROMETHEUS_SANS[@]}"; then
        echo "Skipped generation of Prometheus TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-prometheus" \
            "$PROMETHEUS_KEY" "$PROMETHEUS_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${PROMETHEUS_SANS[@]}"
        echo "Generated Prometheus TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of Prometheus TLS certificate (no SANs specified)"
fi

# Grafana TLS certificate
if [[ ${#GRAFANA_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-grafana"

    GRAFANA_KEY="$CERT_DIR/flightctl-grafana/server.key"
    GRAFANA_CERT="$CERT_DIR/flightctl-grafana/server.crt"

    if [[ -f "$GRAFANA_CERT" ]] && [[ -f "$GRAFANA_KEY" ]] && cert_has_expected_sans "$GRAFANA_CERT" "${GRAFANA_SANS[@]}"; then
        echo "Skipped generation of Grafana TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-grafana" \
            "$GRAFANA_KEY" "$GRAFANA_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${GRAFANA_SANS[@]}"
        echo "Generated Grafana TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of Grafana TLS certificate (no SANs specified)"
fi

# UserInfo Proxy TLS certificate
if [[ ${#USERINFO_PROXY_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-userinfo-proxy"

    USERINFO_PROXY_KEY="$CERT_DIR/flightctl-userinfo-proxy/server.key"
    USERINFO_PROXY_CERT="$CERT_DIR/flightctl-userinfo-proxy/server.crt"

    if [[ -f "$USERINFO_PROXY_CERT" ]] && [[ -f "$USERINFO_PROXY_KEY" ]] && cert_has_expected_sans "$USERINFO_PROXY_CERT" "${USERINFO_PROXY_SANS[@]}"; then
        echo "Skipped generation of UserInfo Proxy TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-userinfo-proxy" \
            "$USERINFO_PROXY_KEY" "$USERINFO_PROXY_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${USERINFO_PROXY_SANS[@]}"
        echo "Generated UserInfo Proxy TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of UserInfo Proxy TLS certificate (no SANs specified)"
fi

# Remote Access TLS certificate
if [[ ${#REMOTE_ACCESS_SANS[@]} -gt 0 ]]; then
    mkdir -p "$CERT_DIR/flightctl-remote-access"

    REMOTE_ACCESS_KEY="$CERT_DIR/flightctl-remote-access/server.key"
    REMOTE_ACCESS_CERT="$CERT_DIR/flightctl-remote-access/server.crt"

    if [[ -f "$REMOTE_ACCESS_CERT" ]] && [[ -f "$REMOTE_ACCESS_KEY" ]] && cert_has_expected_sans "$REMOTE_ACCESS_CERT" "${REMOTE_ACCESS_SANS[@]}"; then
        echo "Skipped generation of Remote Access TLS certificate (already exists with correct SANs)"
    else
        generate_server_cert "flightctl-remote-access" \
            "$REMOTE_ACCESS_KEY" "$REMOTE_ACCESS_CERT" \
            "$FLIGHTCTL_CA_CERT" "$FLIGHTCTL_CA_KEY" \
            "${REMOTE_ACCESS_SANS[@]}"
        echo "Generated Remote Access TLS certificate (2 years, ECDSA P-256)"
    fi
else
    echo "Skipped generation of Remote Access TLS certificate (no SANs specified)"
fi

# CA Bundle
CA_BUNDLE="$CERT_DIR/ca-bundle.crt"

if [[ ${#PAM_ISSUER_SANS[@]} -gt 0 ]] || [[ ${#GATEWAY_SANS[@]} -gt 0 ]]; then
    cat "$FLIGHTCTL_CA_CERT" "$CLIENT_SIGNER_CA_CERT" "$PAM_ISSUER_TOKEN_SIGNER_CA_CERT" > "$CA_BUNDLE"
else
    cat "$FLIGHTCTL_CA_CERT" "$CLIENT_SIGNER_CA_CERT" > "$CA_BUNDLE"
fi
echo "Generated CA Bundle"

# Clean up serial files
rm -f "$CERT_DIR"/*.srl

echo ""
echo "=== Certificate Generation Complete ==="
echo ""

# Create Kubernetes secrets if requested
if [[ "$CREATE_K8S_SECRETS" == "true" ]]; then
    echo "=== Creating Kubernetes Secrets ==="
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

    # Create flightctl-imagebuilder-api-server-tls secret (only if certificate was generated)
    if [[ ${#IMAGEBUILDER_API_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-imagebuilder-api-server-tls"
        $K8S_CLI create secret tls flightctl-imagebuilder-api-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$IMAGEBUILDER_API_CERT" \
            --key="$IMAGEBUILDER_API_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create flightctl-ui-server-tls secret (only if certificate was generated)
    if [[ ${#UI_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-ui-server-tls"
        $K8S_CLI create secret tls flightctl-ui-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$UI_CERT" \
            --key="$UI_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create flightctl-cli-artifacts-server-tls secret (only if certificate was generated)
    if [[ ${#CLI_ARTIFACTS_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-cli-artifacts-server-tls"
        $K8S_CLI create secret tls flightctl-cli-artifacts-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$CLI_ARTIFACTS_CERT" \
            --key="$CLI_ARTIFACTS_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create flightctl-remote-access-server-tls secret (only if certificate was generated)
    if [[ ${#REMOTE_ACCESS_SANS[@]} -gt 0 ]]; then
        echo "Creating secret: flightctl-remote-access-server-tls"
        $K8S_CLI create secret tls flightctl-remote-access-server-tls \
            --namespace="$NAMESPACE" \
            --cert="$REMOTE_ACCESS_CERT" \
            --key="$REMOTE_ACCESS_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -
    fi

    # Create CA bundle Secret (compatible with trust-manager format)
    echo "Creating Secret: flightctl-ca-bundle"
    $K8S_CLI create secret generic flightctl-ca-bundle \
        --namespace="$NAMESPACE" \
        --from-file=ca-bundle.crt="$CA_BUNDLE" \
        --dry-run=client -o yaml | $K8S_CLI apply -f -

    # Copy CA secrets to internal namespace if different from release namespace
    if [[ -n "$INTERNAL_NAMESPACE" ]] && [[ "$INTERNAL_NAMESPACE" != "$NAMESPACE" ]]; then
        echo ""
        echo "=== Copying CA secrets to internal namespace: $INTERNAL_NAMESPACE ==="

        # Copy client-signer-ca secret
        echo "Copying secret: flightctl-client-signer-ca"
        $K8S_CLI create secret tls flightctl-client-signer-ca \
            --namespace="$INTERNAL_NAMESPACE" \
            --cert="$CLIENT_SIGNER_CA_CERT" \
            --key="$CLIENT_SIGNER_CA_KEY" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -

        # Copy CA bundle secret
        echo "Copying secret: flightctl-ca-bundle"
        $K8S_CLI create secret generic flightctl-ca-bundle \
            --namespace="$INTERNAL_NAMESPACE" \
            --from-file=ca-bundle.crt="$CA_BUNDLE" \
            --dry-run=client -o yaml | $K8S_CLI apply -f -

        echo "CA secrets copied successfully to $INTERNAL_NAMESPACE"
    fi

    echo ""
    echo "=== Kubernetes Secret Creation Complete ==="
fi

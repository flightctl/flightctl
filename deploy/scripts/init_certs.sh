#!/bin/bash
set -euo pipefail

CONFIG_FILE="/etc/flightctl/service-config.yaml"
CERT_DIR="/etc/flightctl/pki"
YAML_HELPER="/usr/share/flightctl/yaml_helpers.py"

CERT_METHOD=$(python3 "$YAML_HELPER" extract .global.generateCertificates "$CONFIG_FILE")
if [ "$CERT_METHOD" = "builtin" ]; then
    echo "Certificate generation enabled - creating certificates using openssl"
else
    echo "Certificate generation disabled - skipping certificate creation"
    exit 0
fi

# Get system hostnames
hostname_short=$(hostname)
hostname_fqdn=$(hostname -f || hostname)

# Get host IP addresses
primary_ip=$(ip route get 1.1.1.1 2>/dev/null \
    | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')

host_ips=()
if [ -n "$primary_ip" ]; then
    host_ips+=("$primary_ip")
else
    # Fallback: Get all non-loopback IPs
    mapfile -t host_ips < <(hostname -I 2>/dev/null | tr ' ' '\n' | awk 'NF' || true)
fi
host_ips+=("127.0.0.1")

# Validate the base domain from config, or default to hostname FQDN
base_domain=$(python3 "$YAML_HELPER" extract .global.baseDomain "$CONFIG_FILE")
if [[ -z "$base_domain" ]]; then
    base_domain="$hostname_short"
    echo "global.baseDomain not set, defaulting to system hostname ($base_domain)"
fi

# Build SAN arrays for each certificate type
# SANs include:
#  * External DNS names (based on baseDomain)
#  * System hostnames (short and FQDN)
#  * Podman service names
#  * Loopback address
#  * All host IP addresses

# API certificate SANs
api_sans=(
    "api.$base_domain"
    "agent-api.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-api"
    "localhost"
)
api_sans+=("${host_ips[@]}")

# Telemetry certificate SANs
telemetry_sans=(
    "telemetry.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-telemetry-gateway"
    "localhost"
)
telemetry_sans+=("${host_ips[@]}")

# Alert Manager Proxy certificate SANs
alertmanager_proxy_sans=(
    "alertmanager-proxy.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-alertmanager-proxy"
    "localhost"
)
alertmanager_proxy_sans+=("${host_ips[@]}")

# PAM Issuer certificate SANs
pam_issuer_sans=(
    "pam-issuer.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-pam-issuer"
    "localhost"
)
pam_issuer_sans+=("${host_ips[@]}")

# Build the certificate generation command
cert_gen_args=("--cert-dir" "$CERT_DIR")

for san in "${api_sans[@]}"; do
    cert_gen_args+=("--api-san" "$san")
done

for san in "${telemetry_sans[@]}"; do
    cert_gen_args+=("--telemetry-san" "$san")
done

for san in "${alertmanager_proxy_sans[@]}"; do
    cert_gen_args+=("--alertmanager-proxy-san" "$san")
done

for san in "${pam_issuer_sans[@]}"; do
    cert_gen_args+=("--pam-issuer-san" "$san")
done

# Generate certificates
/usr/share/flightctl/generate-certificates.sh "${cert_gen_args[@]}"

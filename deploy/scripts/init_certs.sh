#!/bin/bash
set -euo pipefail

CONFIG_FILE="/etc/flightctl/service-config.yaml"
CERT_DIR="/etc/flightctl/pki"
ENCRYPTION_DIR="/etc/flightctl/encryption"
YAML_HELPER="/usr/share/flightctl/yaml_helpers.py"

# Generate encryption key unconditionally (independent of certificate method)
/usr/share/flightctl/generate-encryption-key.sh --encryption-dir "$ENCRYPTION_DIR"

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
    # Normalize to lowercase (DNS is case-insensitive per RFC 1123)
    base_domain="${hostname_fqdn,,}"
    echo "global.baseDomain not set, defaulting to system hostname FQDN ($base_domain)"
fi

# Validate as hostname or FQDN: lowercase alphanumerics and hyphens, final label must start with letter
if ! [[ "$base_domain" =~ ^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)*[a-z]([-a-z0-9]*[a-z0-9])?$ ]]; then
    echo "ERROR: global.baseDomain must be a valid hostname or FQDN (not an IP address)" 1>&2
    exit 1
fi

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

# Gateway certificate SANs
gateway_sans=(
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-gateway"
    "localhost"
)
gateway_sans+=("${host_ips[@]}")

# Prometheus certificate SANs
prometheus_sans=(
    "prometheus.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-prometheus"
    "localhost"
)
prometheus_sans+=("${host_ips[@]}")

# Grafana certificate SANs
grafana_sans=(
    "grafana.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-grafana"
    "localhost"
)
grafana_sans+=("${host_ips[@]}")

# Remote Access certificate SANs
remote_access_sans=(
    "remote-access.$base_domain"
    "$base_domain"
    "$hostname_short"
    "$hostname_fqdn"
    "flightctl-remote-access"
    "localhost"
)
remote_access_sans+=("${host_ips[@]}")

# Build the certificate generation command
cert_gen_args=("--cert-dir" "$CERT_DIR")

for san in "${api_sans[@]}"; do
    cert_gen_args+=("--api-san" "$san")
done

for san in "${telemetry_sans[@]}"; do
    cert_gen_args+=("--telemetry-san" "$san")
done

for san in "${gateway_sans[@]}"; do
    cert_gen_args+=("--gateway-san" "$san")
done

for san in "${prometheus_sans[@]}"; do
    cert_gen_args+=("--prometheus-san" "$san")
done

for san in "${grafana_sans[@]}"; do
    cert_gen_args+=("--grafana-san" "$san")
done

for san in "${remote_access_sans[@]}"; do
    cert_gen_args+=("--remote-access-san" "$san")
done

# Generate certificates
/usr/share/flightctl/generate-certificates.sh "${cert_gen_args[@]}"

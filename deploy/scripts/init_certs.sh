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

# Get the system hostname FQDN for use as default and in certificate SANs
hostname_fqdn=$(hostname -f || hostname)

# Get the primary host IP address:
# First try to get the IP address used to reach the internet.
# If that fails (e.g. no internet access), use the first non-loopback IP.
host_ip="$(
  ip route get 1.1.1.1 2>/dev/null \
    | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' \
    || true
)"
if [ -z "$host_ip" ]; then
    host_ip=$(hostname -I | awk '{print $1}')
fi

# Validate the base domain from config, or default to hostname FQDN
base_domain=$(python3 "$YAML_HELPER" extract .global.baseDomain "$CONFIG_FILE")
if [[ -z "$base_domain" ]]; then
    base_domain="$hostname_fqdn"
    echo "global.baseDomain not set, defaulting to system hostname FQDN ($base_domain)"
else
    # Validate as hostname or FQDN: lowercase alphanumerics and hyphens, final label must start with letter
    if ! [[ "$base_domain" =~ ^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)*[a-z]([-a-z0-9]*[a-z0-9])?$ ]]; then
        echo "ERROR: global.baseDomain must be a valid hostname or FQDN (not an IP address)" 1>&2
        exit 1
    fi
fi

# Create certificates with the following SANs:
#  * external DNS names (FQDNs)
#  * internal DNS name (hostname)
#  * Podman service name
#  * loopback name and IP
#  * host primary IP address
/usr/share/flightctl/generate-certificates.sh --cert-dir "$CERT_DIR" \
    --api-san "api.$base_domain" \
    --api-san "agent-api.$base_domain" \
    --api-san "$base_domain" \
    --api-san "$hostname_fqdn" \
    --api-san "flightctl-api" \
    --api-san "localhost" \
    --api-san "127.0.0.1" \
    --api-san "$host_ip" \
    --telemetry-san "telemetry.$base_domain" \
    --telemetry-san "$base_domain" \
    --telemetry-san "$hostname_fqdn" \
    --telemetry-san "flightctl-telemetry-gateway" \
    --telemetry-san "localhost" \
    --telemetry-san "127.0.0.1" \
    --telemetry-san "$host_ip" \
    --alertmanager-proxy-san "alertmanager-proxy.$base_domain" \
    --alertmanager-proxy-san "$base_domain" \
    --alertmanager-proxy-san "$hostname_fqdn" \
    --alertmanager-proxy-san "flightctl-alertmanager-proxy" \
    --alertmanager-proxy-san "localhost" \
    --alertmanager-proxy-san "127.0.0.1" \
    --alertmanager-proxy-san "$host_ip"

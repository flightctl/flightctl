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

# Generate certificates
BASE_DOMAIN=$(python3 "$YAML_HELPER" extract .global.baseDomain "$CONFIG_FILE")
/usr/share/flightctl/generate-certificates.sh --cert-dir "$CERT_DIR" --api-san "$BASE_DOMAIN"

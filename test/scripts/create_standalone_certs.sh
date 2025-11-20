#!/usr/bin/env bash

# This script is used to generate a CA and server certificates for
# standalone deployment using the baseDomain from service-config.yaml
#
# This is only intended for testing purposes

set -x -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/functions

CONFIG_FILE="/etc/flightctl/service-config.yaml"
if [[ ! -f "${CONFIG_FILE}" ]]; then
    echo "Error: Configuration file ${CONFIG_FILE} does not exist"
    exit 1
fi

BASE_DOMAIN=$(yq eval '.global.baseDomain' "${CONFIG_FILE}")
if [[ -z "${BASE_DOMAIN}" || "${BASE_DOMAIN}" == "null" ]]; then
    echo "Error: baseDomain is not set in ${CONFIG_FILE}"
    exit 1
fi

CERT_DIR="bin/standalone-certs/pki"
mkdir -p "${CERT_DIR}"

# Create CA for standalone deployment
openssl genrsa -out "${CERT_DIR}/ca.key" 2048
openssl req -new -x509 -days 365 -key "${CERT_DIR}/ca.key" -out "${CERT_DIR}/ca.crt" -subj "/CN=standalone-ca"

# Generate server key and certificate
openssl genrsa -out "${CERT_DIR}/server.key" 2048
openssl req -new -key "${CERT_DIR}/server.key" -out "${CERT_DIR}/server.csr" -subj "/CN=${BASE_DOMAIN}" -config <(cat "/etc/pki/tls/openssl.cnf" <(printf "[SAN]\nsubjectAltName=IP:${BASE_DOMAIN}"))
openssl x509 -req -days 365 -in "${CERT_DIR}/server.csr" -CA "${CERT_DIR}/ca.crt" -CAkey "${CERT_DIR}/ca.key" -CAcreateserial -out "${CERT_DIR}/server.crt" -extfile <(printf "subjectAltName=IP:${BASE_DOMAIN}")

# Copy the server cert and key to /etc/flightctl/pki
sudo mkdir -p /etc/flightctl/pki/custom
sudo cp "${CERT_DIR}/server.crt" "${CERT_DIR}/server.key" /etc/flightctl/pki/custom/

# Copy the ca cert to /etc/flightctl/pki/custom/auth/ca.crt
sudo mkdir -p /etc/flightctl/pki/custom/auth
sudo cp "${CERT_DIR}/ca.crt" /etc/flightctl/pki/custom/auth/ca.crt

# Write service-config.yaml so custom certs are used
sudo yq eval -i '.global.baseDomainTls.customServerCerts = true' "${CONFIG_FILE}"
sudo yq eval -i '.global.auth.customCaCert = true' "${CONFIG_FILE}"

echo "Standalone certificates created and copied to /etc/flightctl/pki/custom"

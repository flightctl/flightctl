#!/usr/bin/env bash

# This script is used to generate a CA and the necessary certificates for a
# private Docker registry targetting the local IP

set -x -e
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)

CERT_DIR="bin/e2e-certs"
mkdir -p "${CERT_DIR}"
if [[ -f "${CERT_DIR}/ca.key" ]]; then
    echo "CA key already exists, skipping generation"
    exit 0
fi

openssl genrsa -out "${CERT_DIR}/ca.key" 2048
openssl req -new -x509 -days 365 -key "${CERT_DIR}/ca.key" -out "${CERT_DIR}/ca.crt" -subj "/CN=e2e-ca"

openssl x509 -in "${CERT_DIR}/ca.crt" -out "${CERT_DIR}/ca.pem" -outform PEM

openssl genrsa -out "${CERT_DIR}/registry.key" 2048
openssl req -new -key "${CERT_DIR}/registry.key" -out "${CERT_DIR}/registry.csr" -subj "/CN=${IP}"  -config <(cat /etc/ssl/openssl.cnf <(printf "[SAN]\nsubjectAltName=DNS:localhost,IP:${IP}"))
openssl x509 -req -days 365 -in "${CERT_DIR}/registry.csr" -CA "${CERT_DIR}/ca.crt" -CAkey "${CERT_DIR}/ca.key" -CAcreateserial -out "${CERT_DIR}/registry.crt" -extfile <(printf "subjectAltName=DNS:localhost,IP:${IP}")


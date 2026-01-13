#!/bin/bash

set -euo pipefail

# This script creates a test swtpm-localca CA for use with emulated TPMs.
# The CA is used to sign TPM Endorsement Key (EK) certificates.

if [ "$#" -ne 1 ]; then
    echo "Usage: $0 <output-directory>"
    echo "  output-directory: Directory where CA files will be created"
    exit 1
fi

OUTPUT_DIR="$1"

echo "Creating test swtpm CA in ${OUTPUT_DIR}..."

# Create output directory
mkdir -p "${OUTPUT_DIR}"

# Check if CA already exists
if [ -f "${OUTPUT_DIR}/signkey.pem" ] && [ -f "${OUTPUT_DIR}/issuercert.pem" ] && [ -f "${OUTPUT_DIR}/swtpm-localca-rootca-cert.pem" ]; then
    echo "✓ Test swtpm CA already exists in ${OUTPUT_DIR}"
    exit 0
fi

# Create temporary directory for CA generation
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "${TEMP_DIR}"' EXIT

# Generate root CA private key
openssl ecparam -name secp384r1 -genkey -noout -out "${TEMP_DIR}/rootca-key.pem"

# Generate root CA certificate
openssl req -new -x509 -days 3650 -key "${TEMP_DIR}/rootca-key.pem" \
    -out "${OUTPUT_DIR}/swtpm-localca-rootca-cert.pem" \
    -subj "/CN=swtpm-localca-rootca" \
    -sha384

# Generate intermediate CA private key
openssl ecparam -name secp384r1 -genkey -noout -out "${OUTPUT_DIR}/signkey.pem"

# Generate intermediate CA certificate signing request
openssl req -new -key "${OUTPUT_DIR}/signkey.pem" \
    -out "${TEMP_DIR}/intermediate.csr" \
    -subj "/CN=swtpm-localca" \
    -sha384

# Sign intermediate CA certificate with root CA
openssl x509 -req -in "${TEMP_DIR}/intermediate.csr" \
    -CA "${OUTPUT_DIR}/swtpm-localca-rootca-cert.pem" \
    -CAkey "${TEMP_DIR}/rootca-key.pem" \
    -CAcreateserial \
    -out "${OUTPUT_DIR}/issuercert.pem" \
    -days 3650 \
    -sha384 \
    -extensions v3_ca \
    -extfile <(cat <<EOF
[v3_ca]
basicConstraints = CA:TRUE
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always,issuer
EOF
)

# Initialize certificate serial number file
echo "01" > "${OUTPUT_DIR}/certserial"

echo "✓ Test swtpm CA created successfully:"
echo "  - Root CA: ${OUTPUT_DIR}/swtpm-localca-rootca-cert.pem"
echo "  - Intermediate CA: ${OUTPUT_DIR}/issuercert.pem"
echo "  - Signing key: ${OUTPUT_DIR}/signkey.pem"
echo "  - Serial file: ${OUTPUT_DIR}/certserial"

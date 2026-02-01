#!/usr/bin/env bash

# This script is used to generate a CA and the necessary certificates for a
# private Docker registry targetting the local IP

set -x -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/functions

IP=$(get_ext_ip)

function get_registry_hostname() {
    if kubectl get ingresses.config/cluster -o jsonpath={.spec.domain} 1>/dev/null 2>/dev/null; then
        echo "e2eregistry.$(kubectl get ingresses.config/cluster -o jsonpath={.spec.domain})"
    else
        echo "localhost"
    fi
}

REGISTRY_HOST=$(get_registry_hostname)

CERT_DIR="bin/e2e-certs/pki/CA"
mkdir -p "${CERT_DIR}"

# Generate CA if it doesn't exist
if [[ -f "${CERT_DIR}/ca.key" ]]; then
    echo "CA key already exists, skipping CA generation"
else
    echo "Creating CA for e2e tests..."
    openssl genrsa -out "${CERT_DIR}/ca.key" 2048
    openssl req -new -x509 -days 365 -key "${CERT_DIR}/ca.key" -out "${CERT_DIR}/ca.crt" -subj "/CN=e2e-ca"
    openssl x509 -in "${CERT_DIR}/ca.crt" -out "${CERT_DIR}/ca.pem" -outform PEM
fi

# NOTE: Registry cert is now generated at runtime by testcontainers (registry.go)
# This ensures the cert always has the correct current IP in its SAN.
# The CA cert above is injected into VMs during prepare-e2e-test.

# Note: helm chart secrets directory removed - testcontainers now manage E2E infrastructure
# Registry certs remain in bin/e2e-certs/pki/CA/ for local use

# ensure pub/private key for SSH access to agents and git server
mkdir -p bin/.ssh/

# if bin/.ssh/id_rsa exists we just exit
if [ ! -f bin/.ssh/id_rsa ]; then
  echo "bin/.ssh/id_rsa does not exist, creating ssh-keygen"
  ssh-keygen -t rsa -b 4096 -f bin/.ssh/id_rsa -N "" -C "e2e test key"
fi

# SSH keys remain in bin/.ssh/ for testcontainers git server

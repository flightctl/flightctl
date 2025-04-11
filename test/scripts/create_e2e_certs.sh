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
if [[ -e "${CERT_DIR}/ca.key" ]]; then
    echo "CA key already exists, skipping generation"
else
    echo "Reusing existing flightcl CA"
    if [[ -f "${HOME}/.flightctl/certs/ca.key" ]]; then
        # we should be using symlinks here 
        cp "${HOME}/.flightctl/certs/ca.key" "${CERT_DIR}/ca.key"
        cp "${HOME}/.flightctl/certs/ca.crt" "${CERT_DIR}/ca.crt"
        cp "${HOME}/.flightctl/certs/ca.crt" "${CERT_DIR}/ca.pem"
    else 
    # create CA for e2e tests
        openssl genrsa -out "${CERT_DIR}/ca.key" 2048
        openssl req -new -x509 -days 365 -key "${CERT_DIR}/ca.key" -out "${CERT_DIR}/ca.req" -subj "/CN=e2e-ca"
        openssl x509 -in "${CERT_DIR}/ca.req" -out "${CERT_DIR}/ca.crt" -outform PEM
        # we are not consistent - crt and der are both used interchangeably
        ln -s "${CERT_DIR}/ca.crt" "${CERT_DIR}/ca.pem"
    fi
    # generate a key for the registry TLS, and get it signed by the CA via CSR
    openssl genrsa -out "${CERT_DIR}/registry.key" 2048
    openssl req -new -key "${CERT_DIR}/registry.key" -out "${CERT_DIR}/registry.csr" -subj "/CN=${IP}"  -config <(cat "/etc/pki/tls/openssl.cnf" <(printf "[SAN]\nsubjectAltName=DNS:${REGISTRY_HOST},IP:${IP}"))
    openssl x509 -req -days 365 -in "${CERT_DIR}/registry.csr" -CA "${CERT_DIR}/ca.crt" -CAkey "${CERT_DIR}/ca.key" -CAcreateserial -out "${CERT_DIR}/registry.crt" -extfile <(printf "subjectAltName=DNS:${REGISTRY_HOST},IP:${IP}")
fi

# copy the registry key/cert to the secrets directory for the helm charts
cp "${CERT_DIR}/registry".* deploy/helm/e2e-extras/secrets/


# ensure pub/private key for SSH access to agents
mkdir -p bin/.ssh/

# if bin/.ssh/id_rsa exists we just exit
if [ ! -f bin/.ssh/id_rsa ]; then
  echo "bin/.ssh/id_rsa does not exist, creating ssh-keygen"
  ssh-keygen -t rsa -b 4096 -f bin/.ssh/id_rsa -N "" -C "e2e test key"
fi

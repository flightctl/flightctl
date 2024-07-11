#!/usr/bin/env bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)
OUTFILE=bin/agent/etc/flightctl/config.yaml
mkdir -p bin/agent/etc/flightctl/certs
mkdir -p bin/agent/etc/flightctl/data
cp ~/.flightctl/certs/ca.crt bin/agent/etc/flightctl/certs/ca.crt
cp ~/.flightctl/certs/client-enrollment.{crt,key} bin/agent/etc/flightctl/certs/

echo "Creating agent config in ${OUTFILE}"
tee ${OUTFILE} <<EOF
enrollment-service:
  authentication:
    client-certificate: certs/client-enrollment.crt
    client-key: certs/client-enrollment.key
  service:
    certificate-authority: certs/ca.crt
    server: https://${IP}:7443
  enrollment-ui-endpoint: https://${IP}:8080
management-service:
  service:
    server: https://${IP}:7443
    certificate-authority: certs/ca.crt
  authentication:
    client-certificate: certs/client-enrollment.crt
    client-key: certs/client-enrollment.key
spec-fetch-interval: 0m10s
status-update-interval: 0m10s
tpm-path: /dev/tpmrm0
EOF

#!/usr/bin/env bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)
OUTFILE=bin/agent/etc/flightctl/config.yaml
mkdir -p bin/agent/etc/flightctl/certs
cp ~/.flightctl/certs/ca.crt bin/agent/etc/flightctl/certs/ca.crt
cp ~/.flightctl/certs/client-enrollment.{crt,key} bin/agent/etc/flightctl/certs/

echo "Creating agent config in ${OUTFILE}"
tee ${OUTFILE} <<EOF
management-endpoint: https://${IP}:3333
enrollment-endpoint: https://${IP}:3333
enrollment-ui-endpoint: https://${IP}:8080
spec-fetch-interval: 0m10s
status-update-interval: 0m10s
tpm-path: /dev/tpmrm0
EOF

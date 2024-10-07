#!/usr/bin/env bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
$("${SCRIPT_DIR}/../get_endpoints.sh")

OUTFILE=bin/agent/etc/flightctl/config.yaml
mkdir -p bin/agent/etc/flightctl/certs
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
    server: ${FLIGHTCTL_API_ENDPOINT}
  enrollment-ui-endpoint: ${FLIGHTCTL_UI_ENDPOINT}
grpc-management-endpoint: ${FLIGHTCTL_AGENT_GRPC}
spec-fetch-interval: 0m10s
status-update-interval: 0m10s
tpm-path: /dev/tpmrm0
EOF

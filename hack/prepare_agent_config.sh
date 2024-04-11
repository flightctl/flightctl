#!/usr/bin/env bash

mkdir -p bin/agent/etc/flightctl/certs
cp ~/.flightctl/certs/ca.crt bin/agent/etc/flightctl/certs/ca.crt
cp ~/.flightctl/certs/client-enrollment.{crt,key} bin/agent/etc/flightctl/certs/

IP=$(ip route get 1.1.1.1 | grep -oP 'src \K\S+')
echo "Found ethernet interface IP: ${IP}"

echo "Creating agent config"
cat <<EOF > bin/agent/etc/flightctl/config.yaml
management-endpoint: https://${IP}:3333
enrollment-endpoint: https://${IP}:3333
enrollment-ui-endpoint: https://${IP}:8080
spec-fetch-interval: 0m10s
status-update-interval: 0m10s
tpm-path: /dev/tpmrm0
EOF

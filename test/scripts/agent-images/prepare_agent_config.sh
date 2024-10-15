#!/usr/bin/env bash

mkdir -p bin/agent/etc/flightctl/certs

echo Requesting enrollment enrollment certificate/key and config for agent =====
# remove any previous CSR with the same name in case it existed
./bin/flightctl delete csr/client-enrollment || true

./bin/flightctl certificate request -n client-enrollment  -d bin/agent/etc/flightctl/certs/ | tee bin/agent/etc/flightctl/config.yaml

# enforce the agent to fetch the spec and update status every 2 seconds to improve the E2E test speed
cat <<EOF | tee -a  bin/agent/etc/flightctl/config.yaml
spec-fetch-interval: 0m2s
status-update-interval: 0m2s
EOF

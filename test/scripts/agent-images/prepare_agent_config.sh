#!/usr/bin/env bash

mkdir -p bin/agent/etc/flightctl/certs

echo Requesting enrollment enrollment certificate/key and config for agent =====
./bin/flightctl certificate request -n client-enrollment -d bin/agent/etc/flightctl/certs/ | tee bin/agent/etc/flightctl/config.yaml

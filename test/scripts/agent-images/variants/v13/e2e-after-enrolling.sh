#!/usr/bin/env bash
set -euo pipefail

mkdir -p /run/flightctl
while [[ ! -e /run/flightctl/e2e-release-after-enrolling ]]; do
    sleep 1
done
touch /run/flightctl/e2e-after-enrolling

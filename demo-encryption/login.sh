#!/bin/bash
set -euo pipefail

NAMESPACE="${FLIGHTCTL_NS:-flightctl}"

pkill -f "kubectl port-forward.*svc/flightctl-api.*3443" 2>/dev/null || true
sleep 1

kubectl port-forward -n "$NAMESPACE" svc/flightctl-api 3443:3443 &>/dev/null &
sleep 3

pkill -f "kubectl port-forward.*svc/flightctl-ui.*8080" 2>/dev/null || true
sleep 1

kubectl port-forward -n flightctl svc/flightctl-ui 8080:8080 &>/dev/null &
sleep 3

TOKEN=$(kubectl create token flightctl-admin -n "$NAMESPACE" --duration=8760h)
bin/flightctl login https://api.flightctl.localhost:3443 \
  --token "$TOKEN" \
  --insecure-skip-tls-verify

bin/flightctl version

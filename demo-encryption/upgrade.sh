#!/bin/bash
set -euo pipefail

NAMESPACE="${FLIGHTCTL_NS:-flightctl}"
RELEASE="flightctl"
CHART_VERSION="1.3.0-main-376-g1938ad8f"

echo "=== Step 1: Current version ==="
bin/flightctl version 2>/dev/null || echo "(CLI not connected yet)"

echo ""
echo "=== Step 2: Helm upgrade to ${CHART_VERSION} ==="
helm upgrade "$RELEASE" oci://quay.io/flightctl/charts/flightctl \
  --version "$CHART_VERSION" \
  --namespace "$NAMESPACE" \
  --set global.auth.type=k8s \
  --set global.baseDomain=flightctl.localhost \
  --set global.exposeServicesMethod=none

echo ""
echo "=== Step 3: Restart all services ==="
kubectl rollout restart -n "$NAMESPACE" \
  deployment/flightctl-api \
  deployment/flightctl-worker \
  deployment/flightctl-periodic \
  deployment/flightctl-alert-exporter \
  deployment/flightctl-alertmanager-proxy \
  deployment/flightctl-remote-access \
  deployment/flightctl-imagebuilder-api \
  deployment/flightctl-imagebuilder-worker

echo "Waiting for rollouts to complete..."
for deploy in api worker periodic alert-exporter alertmanager-proxy remote-access imagebuilder-api imagebuilder-worker; do
  kubectl rollout status -n "$NAMESPACE" "deployment/flightctl-${deploy}" --timeout=120s
done

echo ""
echo "=== Step 4: Wait for DB migration job ==="
kubectl wait --for=condition=Complete -n "$NAMESPACE" job -l app=flightctl-db-migration --timeout=120s 2>/dev/null || true

echo ""
echo "=== Step 5: Re-establish CLI access ==="
# Kill any existing port-forward
pkill -f "kubectl port-forward.*svc/flightctl-api.*3443" 2>/dev/null || true
sleep 1
kubectl port-forward -n "$NAMESPACE" svc/flightctl-api 3443:3443 &>/dev/null &
sleep 3

TOKEN=$(kubectl create token flightctl-admin -n "$NAMESPACE" --duration=8760h)
bin/flightctl login https://api.flightctl.localhost:3443 \
  --token "$TOKEN" \
  --certificate-authority ~/.flightctl/certs/ca.crt 2>/dev/null || \
bin/flightctl login https://api.flightctl.localhost:3443 \
  --token "$TOKEN" \
  --insecure-skip-tls-verify 2>/dev/null || true

echo ""
echo "=== Step 6: New version ==="
bin/flightctl version

echo ""
echo "=== Upgrade complete ==="
echo "Run show-state.sh to verify encryption migration happened."

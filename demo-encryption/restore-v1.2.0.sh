#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BACKUP_ARCHIVE="${SCRIPT_DIR}/backup-v1.2.0/flightctl-backup-20260803T104523Z.tar.gz"
NAMESPACE="flightctl"
CHART_VERSION="1.2.0"
MAIN_VERSION="1.3.0-main-376-g1938ad8f"

FLIGHTCTL_IMAGES=(
  flightctl-alert-exporter-el9
  flightctl-alertmanager-proxy-el9
  flightctl-api-el9
  flightctl-cli-artifacts-el9
  flightctl-db-setup-el9
  flightctl-imagebuilder-api-el9
  flightctl-imagebuilder-worker-el9
  flightctl-periodic-el9
  flightctl-telemetry-gateway-el9
  flightctl-ui-el9
  flightctl-worker-el9
)
INFRA_IMAGES=(
  quay.io/prometheus/alertmanager:v0.28.1
  quay.io/sclorg/postgresql-16-c9s:20250214
  quay.io/sclorg/redis-7-c9s:20250108
)

if [ ! -f "$BACKUP_ARCHIVE" ]; then
  echo "ERROR: Backup archive not found: $BACKUP_ARCHIVE"
  exit 1
fi

echo "=== Step 1: Pull images for both versions ==="
ALL_IMAGES=()
for img in "${FLIGHTCTL_IMAGES[@]}"; do
  ALL_IMAGES+=("quay.io/flightctl/${img}:${CHART_VERSION}")
  ALL_IMAGES+=("quay.io/flightctl/${img}:${MAIN_VERSION}")
done
for img in "${INFRA_IMAGES[@]}"; do
  ALL_IMAGES+=("$img")
done

for img in "${ALL_IMAGES[@]}"; do
  if podman image exists "$img" 2>/dev/null; then
    echo "  cached: $img"
  else
    echo "  pulling: $img"
    podman pull "$img" --quiet || echo "  skipped (not found): $img"
  fi
done

echo ""
echo "=== Step 2: Delete existing kind cluster ==="
kind delete cluster 2>/dev/null || true
echo "Cluster deleted."

echo ""
echo "=== Step 3: Create fresh kind cluster ==="
kind create cluster
echo "Cluster created."

echo ""
echo "=== Step 4: Load images into kind ==="
LOAD_IMAGES=()
for img in "${ALL_IMAGES[@]}"; do
  if podman image exists "$img" 2>/dev/null; then
    LOAD_IMAGES+=("$img")
  fi
done
if [ ${#LOAD_IMAGES[@]} -gt 0 ]; then
  ARCHIVE="/tmp/flightctl-images.tar"
  echo "  Saving ${#LOAD_IMAGES[@]} images to archive..."
  podman save -m -o "$ARCHIVE" "${LOAD_IMAGES[@]}"
  echo "  Loading archive into kind..."
  kind load image-archive "$ARCHIVE"
  rm -f "$ARCHIVE"
fi
echo "Images loaded."

echo ""
echo "=== Step 5: Install Flight Control v${CHART_VERSION} ==="
helm install flightctl oci://quay.io/flightctl/charts/flightctl \
  --version "$CHART_VERSION" \
  --namespace "$NAMESPACE" \
  --create-namespace \
  --set global.auth.type=k8s \
  --set global.baseDomain=flightctl.localhost \
  --set global.exposeServicesMethod=none \
  --wait --timeout 5m

echo ""
echo "=== Step 6: Wait for all pods to be ready ==="
kubectl wait --for=condition=Ready pods -n "$NAMESPACE" -l 'job-name notin (flightctl-db-migration-1)' --all --timeout=300s

echo ""
echo "=== Step 7: Restore from v1.2.0 backup ==="
bin/flightctl-restore "$BACKUP_ARCHIVE" --namespace "$NAMESPACE"

echo ""
echo "=== Step 8: Wait for pods after restore ==="
kubectl wait --for=condition=Ready -n "$NAMESPACE" deploy/flightctl-api --timeout=120s

echo ""
echo "=== Step 9: Set up CLI access ==="
pkill -f "kubectl port-forward.*svc/flightctl-api.*3443" 2>/dev/null || true
sleep 1
nohup kubectl port-forward -n "$NAMESPACE" svc/flightctl-api 3443:3443 &>/dev/null &
sleep 3

TOKEN=$(kubectl create token flightctl-admin -n "$NAMESPACE" --duration=8760h)
bin/flightctl login https://api.flightctl.localhost:3443 \
  --token "$TOKEN" \
  --certificate-authority ~/.flightctl/certs/ca.crt 2>/dev/null || \
bin/flightctl login https://api.flightctl.localhost:3443 \
  --token "$TOKEN" \
  --insecure-skip-tls-verify 2>/dev/null || true

echo ""
bin/flightctl version

echo ""
echo "=== Restore complete ==="
echo "Flight Control v${CHART_VERSION} is running with pre-upgrade plaintext data."
echo ""
echo "To verify, run: bash ${SCRIPT_DIR}/show-state.sh"

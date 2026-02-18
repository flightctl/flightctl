#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ONLY_DB=
DB_SIZE_PARAMS=
FLAVOR=${FLAVOR:-el9}
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
: "${FLAVORCTL:=${ROOT_DIR}/bin/flavorctl}"

source "${SCRIPT_DIR}"/functions
IP=$(get_ext_ip)

options=$(getopt -o adh --long only-db,db-size:,help -n "$0" -- "$@")
eval set -- "$options"

usage="[--only-db] [db-size=e2e|small-1k|medium-10k]"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set api.enabled=false --set worker.enabled=false --set periodic.enabled=false --set kv.enabled=false --set alertExporter.enabled=false --set alertmanager.enabled=false --set telemetryGateway.enabled=false" ; shift ;;
    -h|--help) echo "Usage: $0 $usage"; exit 0 ;;
    --db-size)
      db_size=$2
      case "$db_size" in
        e2e) DB_SIZE_PARAMS="" ;;
        small-1k) DB_SIZE_PARAMS="--set db.resources.requests.cpu=1 --set db.resources.requests.memory=1Gi --set db.resources.limits.cpu=8 --set db.resources.limits.memory=64Gi" ;;
        medium-10k) DB_SIZE_PARAMS="--set db.resources.requests.cpu=4 --set db.resources.requests.memory=8Gi --set db.resources.limits.cpu=20 --set db.resources.limits.memory=128Gi" ;;
        *) echo "Wrong parameter to --db-size flag: $db_size"; echo "Usage: $0 $usage"; exit 1 ;;
      esac
      shift 2
      ;;
    --) shift; break ;;
    *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

# charttmpl generates values.yaml with correct images per flavor.
# We only need flavorctl here to know which third-party images to preload into kind.
SQL_IMAGE=$(${FLAVORCTL} get "${FLAVOR}" images.db.image)
SQL_TAG=$(${FLAVORCTL} get "${FLAVOR}" images.db.tag)
KV_IMAGE=$(${FLAVORCTL} get "${FLAVOR}" images.kv.image)
KV_TAG=$(${FLAVORCTL} get "${FLAVOR}" images.kv.tag)

kubectl create namespace flightctl-external --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-internal --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-e2e      --context kind-kind 2>/dev/null || true

PULL_SECRET_ARG=""
if [ -n "$IMAGE_PULL_SECRET_PATH" ]; then
  PULL_SECRET_NAME=$(yq .metadata.name "$IMAGE_PULL_SECRET_PATH")
  PULL_SECRET_NAMESPACE=$(yq .metadata.namespace "$IMAGE_PULL_SECRET_PATH")
  if [ "$PULL_SECRET_NAMESPACE" != "flightctl-internal" ]; then
    echo "Namespace for IMAGE_PULL_SECRET_PATH must be flightctl-internal"
    exit 1
  fi
  PULL_SECRET_ARG="--set global.imagePullSecretName=${PULL_SECRET_NAME}"
  kubectl apply -f "$IMAGE_PULL_SECRET_PATH"
fi

# Preload third-party images into kind (not built locally)
kind_load_image "${SQL_IMAGE}:${SQL_TAG}" keep-tar

if [ -z "$ONLY_DB" ]; then
  for suffix in periodic api worker alert-exporter alertmanager-proxy cli-artifacts db-setup telemetry-gateway imagebuilder-api imagebuilder-worker ; do
    quay_image="quay.io/flightctl/flightctl-${suffix}:${FLAVOR}-latest"
    if ! podman image exists "${quay_image}"; then
      echo "ERROR: Required image not found: ${quay_image}"
      echo "Ensure containers were built with: make build-containers FLAVOR=${FLAVOR}"
      exit 1
    fi
    kind_load_image "${quay_image}"
  done

  kind_load_image "${KV_IMAGE}:${KV_TAG}" keep-tar

  CLUSTER_CLI_IMAGE=$(${FLAVORCTL} get "${FLAVOR}" helm.images.cluster-cli.image)
  CLUSTER_CLI_TAG=$(${FLAVORCTL} get "${FLAVOR}" helm.images.cluster-cli.tag)
  kind_load_image "${CLUSTER_CLI_IMAGE}:${CLUSTER_CLI_TAG}" keep-tar
fi

GATEWAY_ARGS=""
if [ "$GATEWAY" ]; then
  GATEWAY_ARGS="--set global.exposeServicesMethod=gateway --set global.gatewayClass=contour-gateway --set global.gatewayPorts.tls=4443 --set global.gatewayPorts.http=4480"
fi

helm dependency build ./deploy/helm/flightctl

helm upgrade --install --namespace flightctl-external \
    --values ./deploy/helm/flightctl/values.dev.yaml \
    --set global.baseDomain=${IP}.nip.io \
    --set global.auth.type=k8s \
    ${ONLY_DB} ${DB_SIZE_PARAMS} ${PULL_SECRET_ARG} ${GATEWAY_ARGS} \
    flightctl ./deploy/helm/flightctl/ --kube-context kind-kind

"${SCRIPT_DIR}"/wait_for_postgres.sh

# Wait for Redis deployment to be ready
kubectl rollout status deployment flightctl-kv -n flightctl-internal -w --timeout=300s --context kind-kind

# Make sure the database is usable from the unit tests
DB_POD=$(kubectl get pod -n flightctl-internal -l flightctl.service=flightctl-db --no-headers -o custom-columns=":metadata.name" --context kind-kind )
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- psql -c 'ALTER ROLE admin WITH SUPERUSER'
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- createdb admin 2>/dev/null|| true


if [ "$ONLY_DB" ]; then
  exit 0
fi


kubectl rollout status deployment flightctl-api -n flightctl-external -w --timeout=300s

# Set namespace for try_login to use correct service account
export FLIGHTCTL_NS=flightctl-external

# attempt to login, it could take some time for API to be stable
for i in {1..60}; do
  if try_login; then
    break
  fi
  if [ $i -eq 60 ]; then
    echo "Failed to login to the API after 60 attempts"
    exit 1
  fi
  sleep 5
done

ensure_organization_set

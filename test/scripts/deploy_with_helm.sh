#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
DB_SIZE_PARAMS=
# If using images from a private registry, specify a path to a Kubernetes Secret yaml for your pull secret (in the flightctl-internal namespace)
# IMAGE_PULL_SECRET_PATH=
SQL_IMAGE="quay.io/sclorg/postgresql-16-c9s"
SQL_VERSION="20250214"
KV_IMAGE="quay.io/sclorg/redis-7-c9s"
KV_VERSION="20250108"

source "${SCRIPT_DIR}"/functions
# Dup stderr to fd 3 for run_on_quadlet command traces (test/scripts/functions); same as e2e_startup.sh.
exec 3>&2
IP=$(get_ext_ip)

# Use external getopt for long options
options=$(getopt -o adh --long only-db,db-size:,help -n "$0" -- "$@")
eval set -- "$options"

usage="[--only-db] [db-size=e2e|small-1k|medium-10k]"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set api.enabled=false --set worker.enabled=false --set periodic.enabled=false --set kv.enabled=false --set alertExporter.enabled=false --set alertmanager.enabled=false --set telemetryGateway.enabled=false" ; shift ;;
    -h|--help) echo "Usage: $0 $usage"; exit 0 ;;
    --db-size)
      db_size=$2
      if [ "$db_size" == "e2e" ]; then
        DB_SIZE_PARAMS=""
      elif [ "$db_size" == "small-1k" ]; then
        DB_SIZE_PARAMS="--set db.resources.requests.cpu=1 --set db.resources.requests.memory=1Gi --set db.resources.limits.cpu=8 --set db.resources.limits.memory=64Gi"
      elif [ "$db_size" == "medium-10k" ]; then
        DB_SIZE_PARAMS="--set db.resources.requests.cpu=4 --set db.resources.requests.memory=8Gi --set db.resources.limits.cpu=20 --set db.resources.limits.memory=128Gi"
      else
        echo "Wrong parameter to --db-size flag: $db_size"
        echo "Usage: $0 $usage"
        exit 1
      fi
      shift 2
      ;;
    --) shift; break ;;
    *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

SQL_ARG="--set db.builtin.image.image=${SQL_IMAGE} --set-string db.builtin.image.tag=${SQL_VERSION}"
KV_ARG="--set kv.image.image=${KV_IMAGE} --set-string kv.image.tag=${KV_VERSION}"

# Function to check if an image exists and return the appropriate image name (hardcoded to EL9)
choose_image() {
    local os_image="$1"
    local fallback_image="$2"

    # Try EL9-specific image first
    if podman image exists "${os_image}:latest" 2>/dev/null || docker image inspect "${os_image}:latest" >/dev/null 2>&1; then
        echo "$os_image"
    elif podman image exists "${fallback_image}:latest" 2>/dev/null || docker image inspect "${fallback_image}:latest" >/dev/null 2>&1; then
        echo "$fallback_image"
        echo "Warning: Using fallback image ${fallback_image} (EL9-specific ${os_image} not found)" >&2
    else
        # Default to EL9-specific image (will be built by the deployment process)
        echo "$os_image"
    fi
}

# Override FlightCtl service images to use localhost registry with EL9 suffix and fallback support
API_IMAGE=$(choose_image "localhost/flightctl-api-el9" "localhost/flightctl-api")
WORKER_IMAGE=$(choose_image "localhost/flightctl-worker-el9" "localhost/flightctl-worker")
PERIODIC_IMAGE=$(choose_image "localhost/flightctl-periodic-el9" "localhost/flightctl-periodic")
ALERT_EXPORTER_IMAGE=$(choose_image "localhost/flightctl-alert-exporter-el9" "localhost/flightctl-alert-exporter")
ALERTMANAGER_PROXY_IMAGE=$(choose_image "localhost/flightctl-alertmanager-proxy-el9" "localhost/flightctl-alertmanager-proxy")
CLI_ARTIFACTS_IMAGE=$(choose_image "localhost/flightctl-cli-artifacts-el9" "localhost/flightctl-cli-artifacts")
DB_SETUP_IMAGE=$(choose_image "localhost/flightctl-db-setup-el9" "localhost/flightctl-db-setup")
TELEMETRY_GATEWAY_IMAGE=$(choose_image "localhost/flightctl-telemetry-gateway-el9" "localhost/flightctl-telemetry-gateway")
IMAGEBUILDER_API_IMAGE=$(choose_image "localhost/flightctl-imagebuilder-api-el9" "localhost/flightctl-imagebuilder-api")
IMAGEBUILDER_WORKER_IMAGE=$(choose_image "localhost/flightctl-imagebuilder-worker-el9" "localhost/flightctl-imagebuilder-worker")

SERVICE_IMAGE_ARGS="--set api.image.image=${API_IMAGE} --set api.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set worker.image.image=${WORKER_IMAGE} --set worker.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set periodic.image.image=${PERIODIC_IMAGE} --set periodic.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set alertExporter.image.image=${ALERT_EXPORTER_IMAGE} --set alertExporter.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set alertmanagerProxy.image.image=${ALERTMANAGER_PROXY_IMAGE} --set alertmanagerProxy.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set cliArtifacts.image.image=${CLI_ARTIFACTS_IMAGE} --set cliArtifacts.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set dbSetup.image.image=${DB_SETUP_IMAGE} --set dbSetup.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set telemetryGateway.image.image=${TELEMETRY_GATEWAY_IMAGE} --set telemetryGateway.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set imageBuilderApi.image.image=${IMAGEBUILDER_API_IMAGE} --set imageBuilderApi.image.tag=latest"
SERVICE_IMAGE_ARGS="$SERVICE_IMAGE_ARGS --set imageBuilderWorker.image.image=${IMAGEBUILDER_WORKER_IMAGE} --set imageBuilderWorker.image.tag=latest"

# helm expects the namespaces to exist, and creating namespaces
# inside the helm charts is not recommended.
kubectl create namespace flightctl-external --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-internal --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-e2e      --context kind-kind 2>/dev/null || true

# if we are only deploying the database, we don't need inject the server container
if [ -z "$ONLY_DB" ]; then

  for suffix in periodic api worker alert-exporter alertmanager-proxy cli-artifacts db-setup telemetry-gateway imagebuilder-api imagebuilder-worker ; do
    kind_load_image localhost/flightctl-${suffix}-el9:latest
  done

  kind_load_image "${KV_IMAGE}:${KV_VERSION}" keep-tar
else
  # Clear service image args when only deploying database
  SERVICE_IMAGE_ARGS=""
fi

if [ ! -z "$IMAGE_PULL_SECRET_PATH" ]; then
  PULL_SECRET_NAME=$(cat "$IMAGE_PULL_SECRET_PATH" | yq .metadata.name)
  PULL_SECRET_NAMESPACE=$(cat "$IMAGE_PULL_SECRET_PATH" | yq .metadata.namespace)

  if [ "$PULL_SECRET_NAMESPACE" != "flightctl-internal" ]; then
    echo "Namespace for IMAGE_PULL_SECRET_PATH must be flightctl-internal"
    exit 1
  fi

  SQL_ARG="$SQL_ARG --set global.imagePullSecretName=${PULL_SECRET_NAME}"
  kubectl apply -f "$IMAGE_PULL_SECRET_PATH"
fi

kind_load_image "${SQL_IMAGE}:${SQL_VERSION}" keep-tar

API_PORT=3443
GATEWAY_ARGS=""
if [ "$GATEWAY" ]; then
  API_PORT=4443
  GATEWAY_ARGS="--set global.exposeServicesMethod=gateway --set global.gatewayClass=contour-gateway --set global.gatewayPorts.tls=4443 --set global.gatewayPorts.http=4480"
fi

# Always deploy with Kubernetes auth
AUTH_ARGS="--set global.auth.type=k8s"

helm dependency build ./deploy/helm/flightctl

# Format IP for DNS: use sslip.io for IPv6 (replace : with -), nip.io for IPv4
if [[ "$IP" == *":"* ]]; then
  BASE_DOMAIN="$(echo $IP | tr ':' '-').sslip.io"
else
  BASE_DOMAIN="${IP}.nip.io"
fi

helm upgrade --install --namespace flightctl-external \
                  --values ./deploy/helm/flightctl/values.dev.yaml \
                  --set global.baseDomain=${BASE_DOMAIN} \
                  ${ONLY_DB} ${DB_SIZE_PARAMS} ${AUTH_ARGS} ${SQL_ARG} ${GATEWAY_ARGS} ${KV_ARG} ${SERVICE_IMAGE_ARGS} flightctl \
              ./deploy/helm/flightctl/ --kube-context kind-kind

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

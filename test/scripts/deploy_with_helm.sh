#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
DB_SIZE_PARAMS=
NO_VALUES=
CHART_PATH="./deploy/helm/flightctl/"
VALUES_PATH="./deploy/helm/flightctl/values.dev.yaml"
IMAGE_REGISTRY="localhost/"
IMAGE_TAG="latest"
# If using images from a private registry, specify a path to a Kubernetes Secret yaml for your pull secret (in the flightctl-internal namespace)
# IMAGE_PULL_SECRET_PATH=
SQL_VERSION=${SQL_VERSION:-"latest"}
SQL_IMAGE=${SQL_IMAGE:-"quay.io/sclorg/postgresql-16-c9s"}
KV_VERSION=${KV_VERSION:-"7.4.1"}
KV_IMAGE=${KV_IMAGE:-"docker.io/redis"}
echo "::group::Preparing the cluster..."
source "${SCRIPT_DIR}"/functions
IP=$(get_ext_ip)

# Use external getopt for long options
options=$(getopt -o adh --long only-db,db-size:,auth,help,chart-path:,values-path:,no-values,image-registry:,image-tag: -n "$0" -- "$@")
eval set -- "$options"

usage="[--only-db] [--db-size=e2e|small-1k|medium-10k] [--chart-path=PATH] [--values-path=PATH] [--no-values] [--image-registry=REGISTRY] [--image-tag=TAG]"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set api.enabled=false --set worker.enabled=false --set periodic.enabled=false --set kv.enabled=false --set alertExporter.enabled=false --set alertmanager.enabled=false --set telemetryGateway.enabled=false" ; shift ;;
    -h|--help) echo "Usage: $0 $usage"; exit 0 ;;
    --chart-path) CHART_PATH="$2" ; shift 2 ;;
    --values-path) VALUES_PATH="$2" ; shift 2 ;;
    --no-values) NO_VALUES=true ; shift ;;
    --image-registry) IMAGE_REGISTRY="$2" ; shift 2 ;;
    --image-tag) IMAGE_TAG="$2" ; shift 2 ;;
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

SQL_ARG="--set db.image.image=${SQL_IMAGE} --set db.image.tag=${SQL_VERSION}"
KV_ARG="--set kv.image.image=${KV_IMAGE} --set kv.image.tag=${KV_VERSION}"

# helm expects the namespaces to exist, and creating namespaces
# inside the helm charts is not recommended.
kubectl create namespace flightctl-external --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-internal --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-e2e      --context kind-kind 2>/dev/null || true

# if we are only deploying the database, we don't need inject the server container
if [ -z "$ONLY_DB" ]; then

  # Load images only if using localhost registry OR latest tag
  if [ "$IMAGE_REGISTRY" == "localhost/" ] || [ "$IMAGE_TAG" == "latest" ]; then
    for suffix in periodic api worker alert-exporter alertmanager-proxy cli-artifacts db-setup telemetry-gateway ; do
      kind_load_image "${IMAGE_REGISTRY%/}/flightctl-${suffix}:${IMAGE_TAG}"
    done
  else
    echo "Using custom registry ${IMAGE_REGISTRY} with tag ${IMAGE_TAG}, skipping local image loading for FlightCtl images"
  fi

  kind_load_image "${KV_IMAGE}:${KV_VERSION}" keep-tar
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

echo "::endgroup::"

echo "::group::Deployment"

API_PORT=3443
GATEWAY_ARGS=""
if [ "$GATEWAY" ]; then
  API_PORT=4443
  GATEWAY_ARGS="--set global.exposeServicesMethod=gateway --set global.gatewayClass=contour-gateway --set global.gatewayPorts.tls=4443 --set global.gatewayPorts.http=4480"
fi

AUTH_ARGS=""
if [ "$AUTH" ]; then
  # Always deploy with Kubernetes auth in this script
  AUTH_TYPE=k8s
  AUTH_ARGS="--set global.auth.type=k8s"
fi


ORGS_ARGS=""
if [ "$ORGS" ]; then
  ORGS_ARGS="--set global.organizations.enabled=true"
fi

# Ensure IMAGE_REGISTRY ends with /
if [[ ! "$IMAGE_REGISTRY" == */ ]]; then
  IMAGE_REGISTRY="${IMAGE_REGISTRY}/"
fi

# Build image override arguments using a loop
IMAGE_ARGS=""
if [ "$IMAGE_REGISTRY" != "localhost/" ] || [ "$IMAGE_TAG" != "latest" ]; then
  for component in api:api cliArtifacts:cli-artifacts worker:worker periodic:periodic alertExporter:alert-exporter alertmanagerProxy:alertmanager-proxy dbSetup:db-setup telemetryGateway:telemetry-gateway; do
    helm_key="${component%:*}"
    image_suffix="${component#*:}"
    IMAGE_ARGS="$IMAGE_ARGS --set ${helm_key}.image.image=${IMAGE_REGISTRY}flightctl-${image_suffix}"
    IMAGE_ARGS="$IMAGE_ARGS --set ${helm_key}.image.tag=${IMAGE_TAG}"
  done
fi

# Check if chart is a packaged .tgz file
if [[ "${CHART_PATH}" == *.tgz ]]; then
  echo "Chart is a packaged .tgz file, skipping dependency build"
else
  helm dependency build "${CHART_PATH}"
fi

VALUES_ARG=""
if [ -z "$NO_VALUES" ]; then
  VALUES_ARG="--values ${VALUES_PATH}"
fi
helm dependency build ./deploy/helm/flightctl

helm upgrade --install --namespace flightctl-external \
                  ${VALUES_ARG} \
                  --set global.baseDomain=${IP}.nip.io \
                  ${ONLY_DB} ${DB_SIZE_PARAMS} ${AUTH_ARGS} ${SQL_ARG} ${GATEWAY_ARGS} ${KV_ARG} ${ORGS_ARGS} ${IMAGE_ARGS} flightctl \
              "${CHART_PATH}" --kube-context kind-kind

echo "::endgroup::"

echo "::group::Check deployment"

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

if [ "$AUTH" ]; then
  echo "Waiting for authentication services to be ready..."
fi

kubectl rollout status deployment flightctl-api -n flightctl-external -w --timeout=300s

LOGGED_IN=false

# attempt to login, it could take some time for API to be stable
for i in {1..60}; do
  if [ "$AUTH" ]; then
    TOKEN=$(kubectl -n flightctl-external create token flightctl-admin --context kind-kind 2>/dev/null || true)
    if [ -n "$TOKEN" ] && ./bin/flightctl login -k https://api.${IP}.nip.io:${API_PORT} --token "$TOKEN"; then
      LOGGED_IN=true
      break
    fi
  else
    if ./bin/flightctl login -k https://api.${IP}.nip.io:${API_PORT}; then
      LOGGED_IN=true
      break
    fi
  fi
  sleep 5
done

if [[ "${LOGGED_IN}" == "false" ]]; then
  echo "Failed to login to the API"
  exit 1
fi

ensure_organization_set

# Setup telemetry gateway certificates (non-blocking)
if ! "${SCRIPT_DIR}"/setup_telemetry_gateway_certs.sh \
  --sans "DNS:localhost,DNS:flightctl-telemetry-gateway.flightctl-external.svc,DNS:flightctl-telemetry-gateway.flightctl-external.svc.cluster.local,DNS:telemetry-gateway.${IP}.nip.io,IP:127.0.0.1" \
  --yaml-helpers-path "./deploy/scripts/yaml_helpers.py" \
  --force-rotate; then
  echo "WARNING: Failed to setup telemetry gateway certificates. Deployment will continue without them."
  echo "You can manually run the certificate setup later if needed."
fi

echo "::endgroup::"

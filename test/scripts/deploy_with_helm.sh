#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
DB_SIZE_PARAMS=
RABBITMQ_VERSION=${RABBITMQ_VERSION:-"3.13"}
RABBITMQ_IMAGE=${RABBITMQ_IMAGE:-"docker.io/rabbitmq"}
VALKEY_VERSION=${VALKEY_VERSION:-"8.0.1"}
VALKEY_IMAGE=${VALKEY_IMAGE:-"docker.io/valkey/valkey"}

source "${SCRIPT_DIR}"/functions
IP=$(get_ext_ip)

# Use external getopt for long options
options=$(getopt -o adh --long only-db,db-size:,auth,help -n "$0" -- "$@")
eval set -- "$options"

usage="[--only-db] [db-size=e2e|small|prod]"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set flightctl.api.enabled=false --set flightctl.worker.enabled=false --set flightctl.periodic.enabled=false --set flightctl.rabbitmq.enabled=false --set flightctl.kv.enabled=false" ; shift ;;
    -h|--help) echo "Usage: $0 $usage"; exit 0 ;;
    --db-size)
      db_size=$2
      if [ "$db_size" == "e2e" ]; then
        DB_SIZE_PARAMS=""
      elif [ "$db_size" == "small" ]; then
        DB_SIZE_PARAMS="--set db.resources.requests.cpu=1 --set db.resources.requests.memory=1Gi --set db.resources.limits.cpu=8 --set db.resources.limits.memory=64Gi"
      elif [ "$db_size" == "prod" ]; then
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

RABBITMQ_ARG="--set flightctl.rabbitmq.image.image=${RABBITMQ_IMAGE} --set flightctl.rabbitmq.image.tag=${RABBITMQ_VERSION}"
VALKEY_ARG="--set flightctl.kv.image.image=${VALKEY_IMAGE} --set flightctl.kv.image.tag=${VALKEY_VERSION}"

# helm expects the namespaces to exist, and creating namespaces
# inside the helm charts is not recommended.
kubectl create namespace flightctl-external --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-internal --context kind-kind 2>/dev/null || true
kubectl create namespace flightctl-e2e      --context kind-kind 2>/dev/null || true

# if we are only deploying the database, we don't need inject the server container
if [ -z "$ONLY_DB" ]; then

  for suffix in periodic api worker ; do
    kind_load_image localhost/flightctl-${suffix}:latest
  done

  kind_load_image "${RABBITMQ_IMAGE}:${RABBITMQ_VERSION}" keep-tar
  kind_load_image "${VALKEY_IMAGE}:${VALKEY_VERSION}" keep-tar
fi



# if we need another database image we can set it with PGSQL_IMAGE (i.e. arm64 images)
if [ ! -z "$PGSQL_IMAGE" ]; then
  DB_IMG="localhost/flightctl-db:latest"
  # we send the image directly to kind, so we don't need to inject the credentials in the kind cluster
  podman pull "${PGSQL_IMAGE}"
  podman tag "${PGSQL_IMAGE}" "${DB_IMG}"
  kind_load_image ${DB_IMG}
  HELM_DB_IMG="--set flightctl.db.image.image=${DB_IMG} --set flightctl.db.image.tag=latest"
fi

API_PORT=3443
KEYCLOAK_PORT=8080
GATEWAY_ARGS=""
if [ "$GATEWAY" ]; then
  API_PORT=4443
  KEYCLOAK_PORT=4480
  GATEWAY_ARGS="--set global.exposeServicesMethod=gateway --set global.gatewayClass=contour-gateway --set global.gatewayPorts.tls=4443 --set global.gatewayPorts.http=4480"
fi

AUTH_ARGS=""
if [ "$AUTH" ]; then
  AUTH_ARGS="--set global.auth.type=builtin --set keycloak.directAccessGrantsEnabled=true"
fi

helm dependency build ./deploy/helm/flightctl

helm upgrade --install --namespace flightctl-external \
                  --values ./deploy/helm/flightctl/values.dev.yaml \
                  --set global.baseDomain=${IP}.nip.io \
                  ${ONLY_DB} ${DB_SIZE_PARAMS} ${AUTH_ARGS} ${HELM_DB_IMG} ${RABBITMQ_ARG} ${GATEWAY_ARGS} ${VALKEY_ARG} flightctl \
              ./deploy/helm/flightctl/ --kube-context kind-kind

kubectl rollout status statefulset flightctl-rabbitmq -n flightctl-internal -w --timeout=300s

"${SCRIPT_DIR}"/wait_for_postgres.sh

# Make sure the database is usable from the unit tests
DB_POD=$(kubectl get pod -n flightctl-internal -l flightctl.service=flightctl-db --no-headers -o custom-columns=":metadata.name" --context kind-kind )
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- psql -c 'ALTER ROLE admin WITH SUPERUSER'
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- createdb admin 2>/dev/null|| true


if [ "$ONLY_DB" ]; then
  exit 0
fi

if [ "$AUTH" ]; then
  kubectl rollout status statefulset keycloak-db -n flightctl-external -w --timeout=300s --context kind-kind
  kubectl rollout status deployment keycloak -n flightctl-external -w --timeout=300s --context kind-kind
fi

kubectl rollout status deployment flightctl-api -n flightctl-external -w --timeout=300s

LOGGED_IN=false
# attempt to login, it could take some time for API to be stable
for i in {1..60}; do
  if [ "$AUTH" ]; then
    PASS=$(kubectl get secret keycloak-demouser-secret -n flightctl-external -o json | jq -r '.data.password' | base64 -d)
    TOKEN=$(curl -d client_id=flightctl -d username=demouser -d password=${PASS} -d grant_type=password http://auth.${IP}.nip.io:${KEYCLOAK_PORT}/realms/flightctl/protocol/openid-connect/token | jq -r '.access_token')
    if ./bin/flightctl login --insecure-skip-tls-verify https://api.${IP}.nip.io:${API_PORT} --token ${TOKEN}; then
      LOGGED_IN=true
      break
    fi
  else
    if ./bin/flightctl login --insecure-skip-tls-verify https://api.${IP}.nip.io:${API_PORT}; then
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


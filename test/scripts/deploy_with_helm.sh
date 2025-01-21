#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
DB_SIZE_PARAMS=
# If using images from a private registry, specify a path to a Kubernetes Secret yaml for your pull secret (in the flightctl-internal namespace)
# IMAGE_PULL_SECRET_PATH=
SQL_VERSION=${SQL_VERSION:-"latest"}
SQL_IMAGE=${SQL_IMAGE:-"quay.io/sclorg/postgresql-12-c8s"}
KV_VERSION=${KV_VERSION:-"7.4.1"}
KV_IMAGE=${KV_IMAGE:-"docker.io/redis"}

source "${SCRIPT_DIR}"/functions
IP=$(get_ext_ip)

# Use external getopt for long options
options=$(getopt -o adh --long only-db,db-size:,auth,help -n "$0" -- "$@")
eval set -- "$options"

usage="[--only-db] [db-size=e2e|small-1k|medium-10k]"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set api.enabled=false --set worker.enabled=false --set periodic.enabled=false --set kv.enabled=false" ; shift ;;
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

SQL_ARG="--set db.image.image=${SQL_IMAGE} --set db.image.tag=${SQL_VERSION}"
KV_ARG="--set kv.image.image=${KV_IMAGE} --set kv.image.tag=${KV_VERSION}"

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

API_PORT=3443
KEYCLOAK_PORT=8081
GATEWAY_ARGS=""
if [ "$GATEWAY" ]; then
  API_PORT=4443
  KEYCLOAK_PORT=4480
  GATEWAY_ARGS="--set global.exposeServicesMethod=gateway --set global.gatewayClass=contour-gateway --set global.gatewayPorts.tls=4443 --set global.gatewayPorts.http=4480"
fi

AUTH_ARGS=""
if [ "$AUTH" ]; then
  AUTH_ARGS="--set global.auth.type=builtin"
fi

helm dependency build ./deploy/helm/flightctl

helm upgrade --install --namespace flightctl-external \
                  --values ./deploy/helm/flightctl/values.dev.yaml \
                  --set global.baseDomain=${IP}.nip.io \
                  ${ONLY_DB} ${DB_SIZE_PARAMS} ${AUTH_ARGS} ${SQL_ARG} ${GATEWAY_ARGS} ${KV_ARG} flightctl \
              ./deploy/helm/flightctl/ --kube-context kind-kind

kubectl rollout status statefulset flightctl-kv -n flightctl-internal -w --timeout=300s

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
    if ./bin/flightctl login -k https://api.${IP}.nip.io:${API_PORT} -u demouser -p ${PASS}; then
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


#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
RABBITMQ_VERSION=${RABBITMQ_VERSION:-"3.13"}
RABBITMQ_IMAGE=${RABBITMQ_IMAGE:-"docker.io/rabbitmq"}

IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)

source "${SCRIPT_DIR}"/functions

# Use external getopt for long options
options=$(getopt -o adh --long only-db,auth,help -n "$0" -- "$@")
eval set -- "$options"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set flightctl.api.enabled=false --set flightctl.worker.enabled=false --set flightctl.periodic.enabled=false --set flightctl.rabbitmq.enabled=false" ; shift ;;
    -h|--help) echo "Usage: $0 [--only-db]"; exit 0 ;;
    --) shift; break ;;
    *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

RABBITMQ_ARG="--set flightctl.rabbitmq.image.image=${RABBITMQ_IMAGE} --set flightctl.rabbitmq.image.tag=${RABBITMQ_VERSION}"

# if we have an existing deployment, try to upgrade it instead
if helm list --kube-context kind-kind -A | grep flightctl > /dev/null; then
  METHOD=upgrade
fi

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

AUTH_ARGS=""
if [ "$AUTH" ]; then
  AUTH_ARGS="--set global.auth.type=builtin --set global.auth.oidcAuthority=http://${IP}:8080/realms/flightctl"
fi

helm dependency build ./deploy/helm/flightctl

helm ${METHOD} --namespace flightctl-external \
                  --values ./deploy/helm/flightctl/values.kind.yaml \
                  --set global.baseDomain=${IP}.nip.io \
                  ${ONLY_DB} ${AUTH_ARGS} ${HELM_DB_IMG} ${RABBITMQ_ARG} flightctl \
              ./deploy/helm/flightctl/ --kube-context kind-kind

kubectl rollout status statefulset flightctl-rabbitmq -n flightctl-internal -w --timeout=300s

"${SCRIPT_DIR}"/wait_for_postgres.sh

# Make sure the database is usable from the unit tests
DB_POD=$(kubectl get pod -n flightctl-internal -l flightctl.service=flightctl-db --no-headers -o custom-columns=":metadata.name" --context kind-kind )
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- psql -c 'ALTER ROLE admin WITH SUPERUSER'
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- createdb admin 2>/dev/null|| true


if [ "$AUTH" ]; then
  kubectl rollout status statefulset keycloak-db -n flightctl-external -w --timeout=300s --context kind-kind
  kubectl rollout status deployment keycloak -n flightctl-external -w --timeout=300s --context kind-kind
fi


# in github CI load docker-image does not seem to work for our images
kind_load_image localhost/git-server:latest
kind_load_image docker.io/library/registry:2

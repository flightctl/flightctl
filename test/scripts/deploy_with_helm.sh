#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

METHOD=install
ONLY_DB=
NO_AUTH=
RABBITMQ_IMAGE=${RABBITMQ_IMAGE:-docker.io/rabbitmq:3.13}
IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)

# Use external getopt for long options
options=$(getopt -o adh --long only-db,no-auth,help -n "$0" -- "$@")
eval set -- "$options"

while true; do
  case "$1" in
    -a|--only-db) ONLY_DB="--set flightctl.api.enabled=false --set flightctl.worker.enabled=false --set flightctl.periodic.enabled=false --set flightctl.rabbitmq.enabled=false" ; shift ;;
    -d|--no-auth) NO_AUTH="--set flightctl.keycloak.enabled=false"; shift ;;
    -h|--help) echo "Usage: $0 [--only-db] [--no-auth]"; exit 0 ;;
    --) shift; break ;;
    *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

RABBITMQ_ARG="--set flightctl.rabbitmq.image=${RABBITMQ_IMAGE}"

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
  # In github CI, kind gets confused and tries to pull the image from docker instead
  # of podman, so if regular docker-image fails we need to:
  #   * save it to OCI image format
  #   * then load it into kind
  for suffix in periodic api worker ; do
    if ! kind load docker-image localhost/flightctl-${suffix}:latest; then
      podman save localhost/flightctl-${suffix}:latest -o flightctl-${suffix}.tar && \
      kind load image-archive flightctl-${suffix}.tar
      rm flightctl-${suffix}.tar
    fi
  done
  podman pull ${RABBITMQ_IMAGE} || true
  podman save ${RABBITMQ_IMAGE} -o rabbitmq.tar
  kind load image-archive rabbitmq.tar
  rm rabbitmq.tar
fi

# if we need another database image we can set it with PGSQL_IMAGE (i.e. arm64 images)
if [ ! -z "$PGSQL_IMAGE" ]; then
  DB_IMG="localhost/flightctl-db:latest"
  # we send the image directly to kind, so we don't need to inject the credentials in the kind cluster
  podman pull "${PGSQL_IMAGE}"
  podman tag "${PGSQL_IMAGE}" "${DB_IMG}"
  kind load docker-image "${DB_IMG}"
  DB_IMG="--set flightctl.db.image=${DB_IMG}"
fi

helm ${METHOD} --values ./deploy/helm/flightctl/values.kind.yaml --set flightctl.api.hostName=${IP}  ${ONLY_DB} ${NO_AUTH} ${DB_IMG} ${RABBITMQ_ARG} flightctl \
              ./deploy/helm/flightctl/ --kube-context kind-kind

kubectl rollout status statefulset flightctl-rabbitmq -n flightctl-internal -w --timeout=300s

"${SCRIPT_DIR}"/wait_for_postgres.sh

# Make sure the database is usable from the unit tests
DB_POD=$(kubectl get pod -n flightctl-internal -l flightctl.service=flightctl-db --no-headers -o custom-columns=":metadata.name" --context kind-kind )
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- psql -c 'ALTER ROLE admin WITH SUPERUSER'
kubectl exec -n flightctl-internal --context kind-kind "${DB_POD}" -- createdb admin 2>/dev/null|| true


if [ -z "$ONLY_DB" ]; then
  mkdir -p  ~/.flightctl/certs

  # Extract .fligthctl files from the api pod, but we must wait for the server to be ready

  kubectl rollout status deployment flightctl-api -n flightctl-external -w --timeout=300s --context kind-kind

  # we actually don't need to download the ca.key or server.key but apparently the flightctl
  # client expects them to be present TODO: fix this in flightctl
  API_POD=$(kubectl get pod -n flightctl-external -l flightctl.service=flightctl-api --no-headers -o custom-columns=":metadata.name" --context kind-kind | head -1 )

  # wait for the server to write the client.yaml file
  until kubectl exec -n flightctl-external "${API_POD}" --context kind-kind  -- cat /root/.flightctl/client.yaml > /dev/null 2>&1;
  do
    sleep 1;
  done

  # pull agent-usable details as well as client configuration file
  for f in certs/{ca.crt,client-enrollment.crt,client-enrollment.key} client.yaml; do
    # a kubectl cp would be more efficient, but tar is not available on the image, and we don't want
    # to switch from ubi9-micro just for tar
    kubectl exec -n flightctl-external "${API_POD}" --context kind-kind  -- cat /root/.flightctl/$f > ~/.flightctl/$f
  done

  chmod og-rwx ~/.flightctl/certs/*.key
fi

# in github CI load docker-image does not seem to work for our images
if ! kind load docker-image localhost/git-server:latest; then
  podman save localhost/git-server:latest -o git-server.tar && \
  kind load image-archive git-server.tar
  rm git-server.tar
fi

# deploy E2E local services for testing: local registry, eventually a git server, ostree repos, etc...
helm ${METHOD} --values ./deploy/helm/e2e-extras/values.kind.yaml flightctl-e2e-extras \
                        ./deploy/helm/e2e-extras/ --kube-context kind-kind

sudo tee /etc/containers/registries.conf.d/flightctl-e2e.conf <<EOF
[[registry]]
location = "${IP}:5000"
insecure = true
[[registry]]
location = "localhost:5000"
insecure = true
EOF
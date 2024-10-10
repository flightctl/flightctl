#!/usr/bin/env bash
set -x -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
METHOD=install
ONLY_DB=
RABBITMQ_VERSION=${RABBITMQ_VERSION:-"3.13"}
RABBITMQ_IMAGE=${RABBITMQ_IMAGE:-"docker.io/rabbitmq"}

IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)


# Function to save images to kind, with workaround for github CI and other environment issues
# In github CI, kind gets confused and tries to pull the image from docker instead
# of podman, so if regular docker-image fails we need to:
#   * save it to OCI image format
#   * then load it into kind
kind_load_image() {
  local image=$1
  local keep_tar=${2:-"do-not-keep-tar"}
  local tar_filename=$(echo $image.tar | sed 's/[:\/]/_/g')

  # First, try to load the image directly
  if kind load docker-image "${image}"; then
    echo "Image ${image} loaded successfully."
    return
  fi

  # If that fails, we have the workaround in place
  if [ -f "${tar_filename}" ] && [ "${keep_tar}" == "keep-tar" ]; then
    echo "File ${tar_filename} already exists. Skipping save."
  else
    echo "Saving ${image} to ${tar_filename}..."

    # If the image is not local we may need to pull it first
    if [[ "${image}" != localhost* ]]; then
      podman pull "${image}"
    fi

    # Save to tar file
    podman save "${image}" -o "${tar_filename}"
    if [ $? -eq 0 ]; then
      echo "Image saved successfully to ${tar_filename}."
    else
      echo "Failed to save image to ${tar_filename}."
      exit 1
    fi
  fi

  kind load image-archive "${tar_filename}"
  if [ "${keep_tar}" != "keep-tar" ]; then
    rm -f "${tar_filename}"
  fi
}

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


if [ -z "$ONLY_DB" ]; then

  if [ "$AUTH" ]; then
    kubectl rollout status statefulset keycloak-db -n flightctl-external -w --timeout=300s --context kind-kind
    kubectl rollout status deployment keycloak -n flightctl-external -w --timeout=300s --context kind-kind
  fi

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

# attempt to login, it could take some time for API to be stable
for i in {1..5}; do
  if ./bin/flightctl login --insecure-skip-tls-verify https://api.${IP}.nip.io:3443; then
    break
  fi
  sleep 5
done

# in github CI load docker-image does not seem to work for our images
kind_load_image localhost/git-server:latest
kind_load_image docker.io/library/registry:2

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

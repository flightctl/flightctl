#!/usr/bin/env bash
set -x -eo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

# make sure we have helm installed
"${SCRIPT_DIR}"/install_helm.sh

if in_kind; then
    ARGS="--values ./deploy/helm/e2e-extras/values.dev.yaml"
    # in github CI load docker-image does not seem to work for our images
    # Note: git-server is built by build-e2e-containers, so we only load the registry here
    kind_load_image quay.io/flightctl/e2eregistry:2
fi

REPOADDR=$(registry_address)

# deploy E2E local services for testing: local registry, eventually a git server, ostree repos, etc...
helm upgrade --install --namespace flightctl-e2e --create-namespace   \
                          ${ARGS} \
                          flightctl-e2e-extras \
                           ./deploy/helm/e2e-extras/

# add the local registry to the registries.conf.d
sudo tee /etc/containers/registries.conf.d/flightctl-e2e.conf <<EOF
[[registry]]
location = "${REPOADDR}"
insecure = true
EOF

if in_kind; then
    sudo tee -a /etc/containers/registries.conf.d/flightctl-e2e.conf <<EOF
[[registry]]
location = "localhost:5000"
insecure = true
EOF
fi

# Set namespace for try_login to use correct service account
export FLIGHTCTL_NS=flightctl-external

# attempt login to flightctl
try_login

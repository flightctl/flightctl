#!/usr/bin/env bash

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

set -x
QUAY_CHARTS=${QUAY_CHARTS:-quay.io/flightctl/charts}
FLIGHTCTL_VERSION=$(${SCRIPT_DIR}/current-version)
FLIGHTCTL_VERSION=${FLIGHTCTL_VERSION#v} # remove the leading v prefix for version

VERSION=${VERSION:-$FLIGHTCTL_VERSION}

echo packaging "${VERSION}"
helm package deploy/helm/flightctl --version "${VERSION}" --app-version "${VERSION}"

#login with helm registry login quay.io -u ${USER} -p ${PASSWORD}
helm push "flightctl-${VERSION}.tgz" oci://${QUAY_CHARTS}/

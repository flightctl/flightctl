#!/usr/bin/env bash
set -x -e -o pipefail

CONTAINER_IMAGES="flightctl-api flightctl-pam-issuer flightctl-worker flightctl-periodic flightctl-alert-exporter cli-artifacts"


GIT_REF=$(git rev-parse HEAD | cut -c1-9)

for image in $CONTAINER_IMAGES; do
    podman tag ${image}:latest quay.io/flightctl/${image}:latest
    podman tag ${image}:latest quay.io/flightctl/${image}:${GIT_REF}
    podman push quay.io/flightctl/${image}:latest
    podman push quay.io/flightctl/${image}:${GIT_REF}
done

#!/usr/bin/env bash
set -x -e

CONTAINER_IMAGES="flightctl-api flightctl-worker flightctl-periodic flightctl-alert-exporter cli-artifacts"


GIT_REF=$(git rev-parse --short HEAD)

for image in $CONTAINER_IMAGES; do
    podman tag ${image}:latest quay.io/flightctl/${image}:latest
    podman tag ${image}:latest quay.io/flightctl/${image}:${GIT_REF}
    podman push quay.io/flightctl/${image}:latest
    podman push quay.io/flightctl/${image}:${GIT_REF}
done

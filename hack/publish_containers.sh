#!/usr/bin/env bash
# Legacy helper. Prefer CI publish via .github/workflows/publish-containers.yaml.
# Keep this list aligned with hack/services.yaml publish:true (or delete this script).
set -x -e

CONTAINER_IMAGES="flightctl-api flightctl-pam-issuer flightctl-worker flightctl-periodic flightctl-alert-exporter flightctl-cli-artifacts flightctl-alertmanager-proxy flightctl-userinfo-proxy flightctl-db-setup flightctl-telemetry-gateway flightctl-imagebuilder-api flightctl-imagebuilder-worker flightctl-remote-access"

GIT_REF=$(git rev-parse --short HEAD)

for image in $CONTAINER_IMAGES; do
    podman tag ${image}:latest quay.io/flightctl/${image}:latest
    podman tag ${image}:latest quay.io/flightctl/${image}:${GIT_REF}
    podman push quay.io/flightctl/${image}:latest
    podman push quay.io/flightctl/${image}:${GIT_REF}
done

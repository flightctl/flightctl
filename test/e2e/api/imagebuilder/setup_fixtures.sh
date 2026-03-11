#!/usr/bin/env bash
# Creates OCI Repository resources in the shared DB for imagebuilder RESTler tests.
# The imagebuilder API validates that repositories referenced in ImageBuild specs
# exist in the database. Since repositories are managed by the core API (different
# port), RESTler cannot create them as part of the test run. This script provisions
# the required fixtures before the test.
set -euo pipefail

CLIENT_CONFIG="${HOME}/.config/flightctl/client.yaml"
TOKEN=$(grep '^ *access-token:' "$CLIENT_CONFIG" | head -1 | sed 's/^ *access-token: *//')
SERVER=$(awk '/^service:/{f=1;next} f&&/server:/{sub(/.*server: */,"");print;exit}' "$CLIENT_CONFIG")
HOST_PORT="${SERVER#https://}"

for name in source-repo dest-repo; do
    curl -sk -X POST "https://${HOST_PORT}/api/v1/repositories" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Flightctl-API-Version: v1beta1" \
        -d "{\"apiVersion\":\"flightctl.io/v1beta1\",\"kind\":\"Repository\",\"metadata\":{\"name\":\"${name}\"},\"spec\":{\"type\":\"oci\",\"registry\":\"quay.io\",\"accessMode\":\"ReadWrite\"}}" \
        -o /dev/null -w '' 2>/dev/null || true
done

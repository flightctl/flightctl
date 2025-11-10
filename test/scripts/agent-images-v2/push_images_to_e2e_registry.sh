#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

# Source functions to get registry address
source "${SCRIPT_DIR}/../functions"
REGISTRY_ADDRESS=$(registry_address)

echo "E2E registry address: ${REGISTRY_ADDRESS}"

# Function to push images with simplified tags
push_images() {
    local image_pattern="$1"
    local target_repo="$2"

    echo "Processing images matching: ${image_pattern}"

    # Find matching images
    mapfile -t images < <(podman images --format "{{.Repository}}:{{.Tag}}" | grep -E "${image_pattern}" || true)

    if [ ${#images[@]} -eq 0 ]; then
        echo "No images found matching pattern: ${image_pattern}"
        return 0
    fi

    for image in "${images[@]}"; do
        # Extract variant from tag (e.g., v2, v3, v8, base)
        if [[ "$image" =~ .*:(.*)-v[0-9]+\.[0-9]+\.[0-9]+-.*$ ]]; then
            variant="${BASH_REMATCH[1]}"
        else
            echo "Skipping image with unexpected format: $image"
            continue
        fi

        # Create e2e registry tag
        e2e_image="${REGISTRY_ADDRESS}/${target_repo}:${variant}"

        echo "Tagging: ${image} → ${e2e_image}"
        podman tag "${image}" "${e2e_image}"

        echo "Pushing: ${e2e_image}"
        podman push "${e2e_image}" --tls-verify=false
    done
}

echo "=========================================="
echo "Pushing Agent Images to E2E Registry"
echo "=========================================="

# Push flightctl-device images (agent images)
push_images "quay\.io/flightctl/flightctl-device:.*-v[0-9]+\.[0-9]+\.[0-9]+-.*" "flightctl-device"

echo "=========================================="
echo "Pushing Sleep App Images to E2E Registry"
echo "=========================================="

# Push sleep-app images
push_images "quay\.io/flightctl/sleep-app:.*-v[0-9]+\.[0-9]+\.[0-9]+-.*" "sleep-app"

echo "=========================================="
echo "Verifying Images in E2E Registry"
echo "=========================================="

# Verify images are in registry
echo "Agent images in registry:"
curl -k "https://${REGISTRY_ADDRESS}/v2/flightctl-device/tags/list" 2>/dev/null || curl "http://${REGISTRY_ADDRESS}/v2/flightctl-device/tags/list" 2>/dev/null || echo "Failed to query agent images"

echo -e "\nSleep app images in registry:"
curl -k "https://${REGISTRY_ADDRESS}/v2/sleep-app/tags/list" 2>/dev/null || curl "http://${REGISTRY_ADDRESS}/v2/sleep-app/tags/list" 2>/dev/null || echo "Failed to query sleep app images"

echo -e "\n✅ All images pushed to E2E registry successfully!"
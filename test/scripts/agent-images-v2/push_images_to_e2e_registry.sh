#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

# Source functions to get registry address
# expects a function registry_address that prints host:port
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/../functions"
REGISTRY_ADDRESS="$(registry_address)"

TLS_VERIFY="${TLS_VERIFY:-false}"   # set to true if your host trusts the registry cert
echo "E2E registry address: ${REGISTRY_ADDRESS} (tls-verify=${TLS_VERIFY})"

# Tag+push helper. Uses --all when src is a manifest list.
push_one() {
  local src_ref="$1" dst_ref="$2"
  echo "Tagging:  ${src_ref} -> ${dst_ref}"
  podman tag "${src_ref}" "${dst_ref}"

  # If src_ref is a manifest list, push with --all, otherwise normal push
  if podman manifest inspect "${src_ref}" >/dev/null 2>&1; then
    echo "Pushing (manifest list): ${dst_ref}"
    podman push --all --tls-verify="${TLS_VERIFY}" "${dst_ref}"
  else
    echo "Pushing: ${dst_ref}"
    podman push --tls-verify="${TLS_VERIFY}" "${dst_ref}"
  fi
}

# Push images that match a repo regex, retagging to target_repo with two tags:
#   <variant> and <variant>-<tag>  where source tag is "<variant>-<tag>"
push_images() {
  local image_regex="$1"      # grep -E against "REPOSITORY:TAG"
  local target_repo="$2"      # path on the local registry, e.g. "flightctl/flightctl-device"

  echo "Processing images matching: ${image_regex}"
  mapfile -t images < <(podman images --format "{{.Repository}}:{{.Tag}}" | grep -E "${image_regex}" || true)

  if [[ ${#images[@]} -eq 0 ]]; then
    echo "No images found for pattern: ${image_regex}"
    return 0
  fi

  for src in "${images[@]}"; do
    # Split out tag
    tag="${src##*:}"
    # Expect tags like "<variant>-vX.Y.Z..." -> capture variant and the rest starting with v
    if [[ "$tag" =~ ^([^:]+)-(v[0-9]+\.[0-9]+\.[0-9].*)$ ]]; then
      variant="${BASH_REMATCH[1]}"
      tag_suffix="${BASH_REMATCH[2]}"
    else
      echo "Skipping unexpected tag format: ${src}"
      continue
    fi

    dst_base="${REGISTRY_ADDRESS}/${target_repo}:${variant}"
    dst_with_tag="${REGISTRY_ADDRESS}/${target_repo}:${variant}-${tag_suffix}"

    push_one "${src}" "${dst_base}"
    push_one "${src}" "${dst_with_tag}"
  done

  # Verify via registry API
  echo "Listing tags for ${target_repo}:"
  curl -fsSk "https://${REGISTRY_ADDRESS}/v2/${target_repo}/tags/list" \
    || curl -fsS "http://${REGISTRY_ADDRESS}/v2/${target_repo}/tags/list" \
    || echo "Failed to query tags for ${target_repo}"
  echo
}

echo "=========================================="
echo "Pushing Agent Images to E2E Registry"
echo "=========================================="
# Examples of source refs:
# quay.io/flightctl/flightctl-device:base-v1.2.3-abc
# quay.io/flightctl/flightctl-device:v1-v1.2.3-abc
push_images "^quay\.io/flightctl/flightctl-device:base-v[0-9]+\.[0-9]+\.[0-9].*" "flightctl/flightctl-device"
push_images "^quay\.io/flightctl/flightctl-device:base-v[0-9]+\.[0-9]+\.[0-9].*" "flightctl-device"
push_images "^quay\.io/flightctl/flightctl-device:.*-v[0-9]+\.[0-9]+\.[0-9].*" "flightctl/flightctl-device"
push_images "^quay\.io/flightctl/flightctl-device:.*-v[0-9]+\.[0-9]+\.[0-9].*" "flightctl-device"

echo "=========================================="
echo "Pushing Sleep App Images to E2E Registry"
echo "=========================================="
# Example:
# quay.io/flightctl/sleep-app:base-v1.2.3-abc  or v1-v1.2.3-abc
push_images "^quay\.io/flightctl/sleep-app:.*-v[0-9]+\.[0-9]+\.[0-9].*" "sleep-app"

echo "✅ Done"

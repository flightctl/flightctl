#!/usr/bin/env bash
set -euo pipefail

# Exercise cache selection without requiring a privileged Mock build.
source "$(dirname "${BASH_SOURCE[0]}")/build_rpms.sh"

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
original_script_dir="$SCRIPT_DIR"
cp "$SCRIPT_DIR"/{Containerfile.packit_builder,mock-site-defaults.cfg,mock-go-repo.cfg,mock-roots.conf,mock_prewarm_caches.sh} "$test_dir/"
SCRIPT_DIR="$test_dir"
expected_hash=$(builder_inputs_hash)

# The fingerprint must be independent of the caller's working directory.
cd /
[[ "$(builder_inputs_hash)" == "$expected_hash" ]]

IMAGE_REPO=example.invalid/packit-builder
BASE_IMAGE="${IMAGE_REPO}:base"
rebuilds=0
rebuild_images() { rebuilds=$((rebuilds + 1)); }
podman() {
  [[ "$1 $2" == 'image inspect' ]] || return 1
  [[ "${@: -1}" == "$expected_image" ]] || return 1
  printf '%s\n' "$cached_hash"
}

# A matching base image is reusable, while older images without a label rebuild.
ROOT=''
expected_image="$BASE_IMAGE"
cached_hash="$expected_hash"
ensure_current_builder
[[ "$rebuilds" == 0 ]]
cached_hash='<no value>'
ensure_current_builder
[[ "$rebuilds" == 1 ]]

# A known Mock root must validate its own image, rather than the base image.
ROOT=epel-10-x86_64
MOCK_ROOTS_CONFIG="$test_dir/mock-roots.conf"
expected_image="$(root_image_for "$ROOT")"
cached_hash="$expected_hash"
ensure_current_builder
[[ "$rebuilds" == 1 ]]
cached_hash=old-inputs
ensure_current_builder
[[ "$rebuilds" == 2 ]]

# A repository/configuration change invalidates a previously matching image too.
cached_hash="$expected_hash"
printf '\n# Changed build input\n' >> "$SCRIPT_DIR/mock-go-repo.cfg"
ensure_current_builder
[[ "$rebuilds" == 3 ]]

# Unknown roots use the base image and the current input fingerprint.
ROOT=custom-root
expected_image="$BASE_IMAGE"
cached_hash="$(builder_inputs_hash)"
ensure_current_builder
[[ "$rebuilds" == 3 ]]

SCRIPT_DIR="$original_script_dir"
echo 'RPM builder cache selection tests passed'

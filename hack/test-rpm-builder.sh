#!/usr/bin/env bash
set -euo pipefail

# Exercise cache selection without requiring a privileged Mock build.
source "$(dirname "${BASH_SOURCE[0]}")/build_rpms.sh"

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
cp "$SCRIPT_DIR"/{Containerfile.packit_builder,mock-site-defaults.cfg,mock-go-repo.cfg,mock-roots.conf,mock_prewarm_caches.sh} "$test_dir/"
SCRIPT_DIR="$test_dir"
MOCK_ROOTS_CONFIG="$test_dir/mock-roots.conf"
expected_hash=$(builder_inputs_hash)

# Both hashing and rebuilding must work outside the checkout without changing cwd.
cd /
[[ "$(builder_inputs_hash)" == "$expected_hash" ]]
IMAGE_REPO=example.invalid/packit-builder
BASE_IMAGE="${IMAGE_REPO}:base"
builds=()
podman() {
  if [[ "$1" == build ]]; then
    local containerfile='' previous='' argument
    for argument in "$@"; do
      if [[ "$previous" == -f ]]; then containerfile="$argument"; fi
      previous="$argument"
    done
    [[ -f "$containerfile" ]] || return 1
    [[ "$(realpath "$containerfile")" == "$SCRIPT_DIR/Containerfile.packit_builder" ]] || return 1
    [[ "${@: -1}" == "$SCRIPT_DIR" ]] || return 1
    builds+=(base)
    return 0
  fi
  [[ "$1 $2" == 'image inspect' ]] || return 1
  [[ "${@: -1}" == "$expected_image" ]] || return 1
  printf '%s\n' "$cached_hash"
}
build_root_cache_image() { builds+=("root:$1"); }

# A matching base is reused; automatic refresh of a stale base needs no Mock cache.
ROOT=''
expected_image="$BASE_IMAGE"
cached_hash="$expected_hash"
ensure_current_builder
[[ "${#builds[@]}" == 0 ]]
cached_hash='<no value>'
ensure_current_builder
[[ "${builds[*]}" == base ]]
[[ "$PWD" == / ]]

# A known Mock root refreshes only the base and that root's cache.
builds=()
ROOT=epel-10-x86_64
expected_image="$(root_image_for "$ROOT")"
cached_hash="$expected_hash"
ensure_current_builder
[[ "${#builds[@]}" == 0 ]]
cached_hash=old-inputs
ensure_current_builder
[[ "${builds[*]}" == "base root:$ROOT" ]]

# Repository/configuration changes invalidate a previously matching image too.
builds=()
cached_hash="$expected_hash"
printf '\n# Changed build input\n' >> "$SCRIPT_DIR/mock-go-repo.cfg"
ensure_current_builder
[[ "${builds[*]}" == "base root:$ROOT" ]]

# Unknown roots use only the base, including when it needs refreshing.
builds=()
ROOT=custom-root
expected_image="$BASE_IMAGE"
cached_hash="$(builder_inputs_hash)"
ensure_current_builder
[[ "${#builds[@]}" == 0 ]]
cached_hash=old-inputs
ensure_current_builder
[[ "${builds[*]}" == base ]]

# Explicit full rebuilds still prewarm every configured root.
builds=()
ROOT=''
rebuild_images
expected='base'
while IFS= read -r root; do expected+=" root:$root"; done < <(read_mock_roots)
[[ "${builds[*]}" == "$expected" ]]
[[ "$PWD" == / ]]
echo 'RPM builder cache selection tests passed'

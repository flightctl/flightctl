#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/../functions"
REGISTRY_ADDRESS="$(registry_address)"

CONCURRENCY="${CONCURRENCY:-$(nproc || echo 4)}"
TLS_VERIFY="${TLS_VERIFY:-false}"         # set true if your host trusts the registry CA
USE_CRANE_TAG="${USE_CRANE_TAG:-1}"       # 1 = create short tag on the registry with crane tag

echo "[info] E2E registry: ${REGISTRY_ADDRESS}  tls-verify=${TLS_VERIFY}  jobs=${CONCURRENCY}"

has_cmd() { command -v "$1" >/dev/null 2>&1; }

# Build task lines "SRC  DST_WITH_TAG  DST_BASE"
queue_tasks() {
  local image_regex="$1" target_repo="$2"
  echo "[info] Collecting: ${image_regex} -> ${target_repo}"
  mapfile -t images < <(podman images --format "{{.Repository}}:{{.Tag}}" | grep -E "${image_regex}" || true)
  [[ ${#images[@]} -gt 0 ]] || { echo "[warn] No matches for ${image_regex}"; return 0; }

  for src in "${images[@]}"; do
    local tag="${src##*:}" variant tag_suffix
    # expect tags like "<variant>-vX.Y.Z-..."  examples: base-v1.2.3-abc  v1-v1.2.3-abc
    if [[ "$tag" =~ ^([^:]+)-(v[0-9]+\.[0-9]+\.[0-9].*)$ ]]; then
      variant="${BASH_REMATCH[1]}"
      tag_suffix="${BASH_REMATCH[2]}"
    else
      echo "[warn] Skip unexpected tag format: ${src}"
      continue
    fi
    local dst_with_tag="${REGISTRY_ADDRESS}/${target_repo}:${variant}-${tag_suffix}"
    local dst_base="${REGISTRY_ADDRESS}/${target_repo}:${variant}"
    printf '%s %s %s\n' "$src" "$dst_with_tag" "$dst_base"
  done
}

TASKS_FILE="$(mktemp)"; trap 'rm -f "$TASKS_FILE"' EXIT
queue_tasks '^quay\.io/flightctl/flightctl-device:.*-v[0-9]+\.[0-9]+\.[0-9].*' 'flightctl/flightctl-device' >>"$TASKS_FILE"
queue_tasks '^quay\.io/flightctl/sleep-app:.*-v[0-9]+\.[0-9]+\.[0-9].*'     'sleep-app'                 >>"$TASKS_FILE"

# Split into two simple arg lists to keep xargs clean
COPY_LIST="$(mktemp)";  trap 'rm -f "$COPY_LIST"' EXIT
TAG_LIST="$(mktemp)";   trap 'rm -f "$TAG_LIST"'  EXIT

while read -r SRC DST_WITH_TAG DST_BASE; do
  printf '%s %s\n'  "$SRC" "$DST_WITH_TAG" >>"$COPY_LIST"
  printf '%s %s\n'  "$DST_WITH_TAG" "${DST_BASE##*:}" >>"$TAG_LIST"
done <"$TASKS_FILE"

echo "[info] Queued $(wc -l <"$COPY_LIST") uploads"

# Upload variant-vX.Y.Z... in parallel, quiet output
xargs -P "$CONCURRENCY" -n 2 -- \
  bash -c '
    set -euo pipefail
    SRC="$1"; DST="$2"
    skopeo copy --dest-precompute-digests -q --all --dest-tls-verify='"$TLS_VERIFY"' \
      "containers-storage:${SRC}" "docker://${DST}"
  ' _ < "$COPY_LIST"

# Create short tags for the same manifests
if [[ "$USE_CRANE_TAG" = "1" ]] && has_cmd crane; then
  echo "[info] Tagging on registry with crane"
  xargs -P "$CONCURRENCY" -n 2 -- \
    bash -c '
      set -euo pipefail
      FROM="$1"; NEWTAG="$2"
      crane tag "$FROM" "$NEWTAG"
    ' _ < "$TAG_LIST"
else
  echo "[info] crane not available or disabled, pushing short tags too"
  # Rebuild a list of SRC DST_BASE and push quietly
  while read -r SRC DST_WITH_TAG DST_BASE; do
    printf '%s %s\n' "$SRC" "$DST_BASE"
  done <"$TASKS_FILE" \
  | xargs -P "$CONCURRENCY" -n 2 -- \
      bash -c '
        set -euo pipefail
        SRC="$1"; DST="$2"
        skopeo copy -q --all --dest-tls-verify='"$TLS_VERIFY"' \
          "containers-storage:${SRC}" "docker://${DST}"
      ' _
fi

# Optional verify
for repo in "flightctl/flightctl-device" "sleep-app"; do
  echo "[info] Tags for ${repo}:"
  curl -fsSk "https://${REGISTRY_ADDRESS}/v2/${repo}/tags/list" \
    || curl -fsS  "http://${REGISTRY_ADDRESS}/v2/${repo}/tags/list" \
    || echo "[warn] Failed to query ${repo}"
  echo
done

echo "✅ Done"

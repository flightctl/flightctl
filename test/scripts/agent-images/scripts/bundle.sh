#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

OS_ID="${OS_ID:?OS_ID is required}"
TAG="${TAG:-latest}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
VARIANTS="${VARIANTS:-v2 v3 v4 v5 v6 v7 v8 v9 v10}"

ART_DIR="${ROOT_DIR}/artifacts"
OUT_TAR="${ART_DIR}/agent-images-bundle-${OS_ID}.tar"

mkdir -p "${ART_DIR}"

declare -a refs=()

if [ -f "${BASE_DIR}/flavors/${OS_ID}.conf" ]; then
  # shellcheck source=/dev/null
  source "${BASE_DIR}/flavors/${OS_ID}.conf"
fi

variants_list="${VARIANTS}"
if [ -n "${ONLY_VARIANTS:-}" ]; then
  variants_list="${ONLY_VARIANTS}"
fi
if [ -n "${EXCLUDE_VARIANTS:-}" ]; then
  tmp=""
  for v in ${variants_list}; do
    skip=0
    for ex in ${EXCLUDE_VARIANTS}; do
      if [ "$v" = "$ex" ]; then skip=1; break; fi
    done
    [ $skip -eq 0 ] && tmp="${tmp} ${v}"
  done
  variants_list="$(echo "${tmp}" | xargs -n999 echo)"
fi

collect_tags() {
  local name="$1" # base or vN
  local canonical="${IMAGE_REPO}:${name}-${OS_ID}-${TAG}"
  local plain="${IMAGE_REPO}:${name}"
  local os_tag="${IMAGE_REPO}:${name}-${OS_ID}"
  local ver_tag="${IMAGE_REPO}:${name}-${TAG}"

  if podman image exists "${canonical}" >/dev/null 2>&1; then
    refs+=("${plain}" "${os_tag}" "${ver_tag}" "${canonical}")
  else
    echo "Skipping ${canonical} (not built)"
  fi
}

# base
collect_tags "base"

# variants
for v in ${variants_list}; do
  collect_tags "${v}"
done

echo -e "\033[32mSaving bundle to ${OUT_TAR}\033[m"
podman save --multi-image-archive -o "${OUT_TAR}" "${refs[@]}"
echo -e "\033[32mBundle created: ${OUT_TAR}\033[m"



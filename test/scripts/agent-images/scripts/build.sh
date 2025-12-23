#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
CACHE_IMAGE_REPO="${CACHE_IMAGE_REPO:-quay.io/flightctl-tests/flightctl-device-cache}"
APP_REPO="${APP_REPO:-quay.io/flightctl}"
AGENT_OS_ID="${AGENT_OS_ID:-cs9-bootc}"
VARIANTS="${VARIANTS:-v2 v3 v4 v5 v6 v7 v8 v9 v10}"

SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(${ROOT_DIR}/hack/current-version)}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$(cd "${ROOT_DIR}" && ( ( [ ! -d ".git" ] || git diff --quiet ) && echo "clean" ) || echo "dirty")}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(cd "${ROOT_DIR}" && git rev-parse --short "HEAD^{commit}" 2>/dev/null) || echo "unknown"}"
TAG="${TAG:-$SOURCE_GIT_TAG}"

PODMAN_LOG_LEVEL="${PODMAN_LOG_LEVEL:-info}"
PODMAN_BUILD_EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS:-}"
CACHE_MODE="${CACHE_MODE:-use}"          # use | disable | populate
CACHE_TTL="${CACHE_TTL:-168h}"           # 7 days

JOBS="${JOBS:-$(command -v nproc >/dev/null && nproc || echo 1)}"

# CLI args
BUILD_BASE=false
BUILD_VARIANTS=false
BUILD_APPS=false

# If no options specified, default to building base only
if [ $# -eq 0 ]; then
  BUILD_BASE=true
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base)
      BUILD_BASE=true
      shift
      ;;
    --variants)
      BUILD_VARIANTS=true
      shift
      ;;
    --apps)
      BUILD_APPS=true
      shift
      ;;
    --jobs)
      JOBS="$2"
      shift 2
      ;;
    --cache)
      CACHE_MODE="$2"
      case "${CACHE_MODE}" in
        use|disable|populate) ;;
        *)
          echo "Invalid cache mode: ${CACHE_MODE}. Valid values: use, disable, populate" >&2
          exit 1
          ;;
      esac
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      echo "Usage: $0 [--base] [--variants] [--apps] [--jobs N] [--cache use|disable|populate]" >&2
      exit 1
      ;;
  esac
done

PODMAN_BUILD_FLAGS=(--jobs "${JOBS}" --log-level "${PODMAN_LOG_LEVEL}" --pull=missing --layers=true --network=host)

# cache flags for base + variants
CACHE_FLAGS=()
case "${CACHE_MODE}" in
  use)
    CACHE_FLAGS=(--layers --cache-from "${CACHE_IMAGE_REPO}" --cache-ttl "${CACHE_TTL}")
    ;;
  populate)
    CACHE_FLAGS=(--layers --cache-to "${CACHE_IMAGE_REPO}" --cache-ttl "${CACHE_TTL}")
    ;;
  disable)
    CACHE_FLAGS=()
    ;;
esac

echo "Build configuration:"
echo "  BUILD_BASE: ${BUILD_BASE}"
echo "  BUILD_VARIANTS: ${BUILD_VARIANTS}"
echo "  BUILD_APPS: ${BUILD_APPS}"
echo "  AGENT_OS_ID: ${AGENT_OS_ID}"
echo "  TAG: ${TAG}"
echo "  IMAGE_REPO: ${IMAGE_REPO}"
echo "  APP_REPO: ${APP_REPO}"
echo "  JOBS: ${JOBS}"
echo "  CACHE_MODE: ${CACHE_MODE}"
echo "  CACHE_IMAGE_REPO: ${CACHE_IMAGE_REPO}"
echo "  CACHE_TTL: ${CACHE_TTL}"

cd "$ROOT_DIR"

flavor_conf="${BASE_DIR}/flavors/${AGENT_OS_ID}.conf"

if [ ! -f "${flavor_conf}" ]; then
  echo "[ERROR] Flavor configuration not found: ${flavor_conf}" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "${flavor_conf}"
: "${OS_ID:?OS_ID must be set in flavor conf}"
: "${DEVICE_BASE_IMAGE:?DEVICE_BASE_IMAGE must be set in flavor conf}"

base_img_canonical="${IMAGE_REPO}:base-${OS_ID}-${TAG}"
base_img_plain="${IMAGE_REPO}:base"
base_img_os="${IMAGE_REPO}:base-${OS_ID}"
base_img_ver="${IMAGE_REPO}:base-${TAG}"

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

  if [ "${BUILD_BASE}" = "true" ]; then
    echo -e "\033[32m[${OS_ID}] Building base ${base_img_canonical}\033[m"

    podman build "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS} \
      "${CACHE_FLAGS[@]}" \
      --log-level "${PODMAN_LOG_LEVEL}" \
      --build-context "project-bin=${ROOT_DIR}/bin" \
      --build-context "variant-context=${BASE_DIR}/base"\
      --build-arg-file "${flavor_conf}" \
      --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
      --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
      --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
      --label "io.flightctl.e2e.component=device" \
      -f "${BASE_DIR}/base/Containerfile" \
      -t "${base_img_canonical}" \
      -t "${base_img_plain}" \
      -t "${base_img_os}" \
      -t "${base_img_ver}" \
      "${BASE_DIR}"
  fi

  if [ "${BUILD_VARIANTS}" = "true" ]; then
    # Ensure the base image exists locally to avoid registry pulls
    if ! podman image exists "${base_img_canonical}"; then
      echo "[ERROR] Base image not found locally: ${base_img_canonical}" >&2
      echo "        Cannot build variants without base image." >&2
      echo "        Run with --base first or ensure base image exists." >&2
      exit 1
    fi
    echo -e "\033[32m[${OS_ID}] Building variants: ${variants_list}\033[m"

    # Join build flags into strings for safe interpolation in xargs
    PODMAN_BUILD_FLAGS_JOINED=""
    if [ "${#PODMAN_BUILD_FLAGS[@]}" -gt 0 ]; then
      for f in "${PODMAN_BUILD_FLAGS[@]}"; do
        PODMAN_BUILD_FLAGS_JOINED+=" ${f}"
      done
    fi

    CACHE_FLAGS_JOINED=""
    if [ "${#CACHE_FLAGS[@]}" -gt 0 ]; then
      for f in "${CACHE_FLAGS[@]}"; do
        CACHE_FLAGS_JOINED+=" ${f}"
      done
    fi

    EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS:-}"

    export IMAGE_REPO TAG OS_ID BASE_DIR ROOT_DIR base_img_canonical PODMAN_BUILD_FLAGS_JOINED EXTRA_FLAGS CACHE_FLAGS_JOINED
    printf '%s\n' ${variants_list} | xargs -n1 -P "${JOBS}" -I {} bash -lc '\
      set -euo pipefail; \
      v_img_canonical="${IMAGE_REPO}:{}-'"${OS_ID}"'-'"${TAG}"'"; \
      v_img_plain="${IMAGE_REPO}:{}"; \
      v_img_os="${IMAGE_REPO}:{}-'"${OS_ID}"'"; \
      v_img_ver="${IMAGE_REPO}:{}-'"${TAG}"'"; \
      prefix() { while IFS= read -r line; do printf "[%s]\t%s\n" "$v_img_canonical" "$line"; done; }; \
      printf "\033[32m['"${OS_ID}"'] Building variant %s\033[m\n" "$v_img_canonical" | prefix; \
      podman build '"${PODMAN_BUILD_FLAGS_JOINED}"' --pull=never '"${EXTRA_FLAGS}"' '"${CACHE_FLAGS_JOINED}"' \
        --build-context "project-root='"${ROOT_DIR}"'" \
        --build-context "variant-context='"${BASE_DIR}"'/variants/{}" \
        --build-context "common='"${BASE_DIR}"'/common" \
        --build-arg BASE_IMAGE="'"${base_img_canonical}"'" \
        --label "io.flightctl.e2e.component=app" \
        -f "'"${BASE_DIR}"'/variants/{}/Containerfile" \
        -t "$v_img_canonical" \
        -t "$v_img_plain" \
        -t "$v_img_os" \
        -t "$v_img_ver" \
        "'"${BASE_DIR}"'" 2>&1 | prefix'
  fi

# Build apps if requested
if [ "${BUILD_APPS}" = "true" ]; then
  echo -e "\033[32mBuilding app images\033[m"
  
  # Auto-detect app Containerfiles in apps directory
  # Pattern: Containerfile.<app-name>.<version>
  for containerfile in "${BASE_DIR}/apps/Containerfile."*; do
    [ -e "${containerfile}" ] || continue
    
    filename=$(basename "${containerfile}")
    
    # Extract app name and version from Containerfile.<app-name>.<version>
    # Remove "Containerfile." prefix
    name_version="${filename#Containerfile.}"
    
    # Extract version (everything after last dot)
    version="${name_version##*.}"
    
    # Extract app name (everything before last dot)
    app_name="${name_version%.*}"
    
    if [ -z "${app_name}" ] || [ -z "${version}" ]; then
      echo "Warning: Could not parse app name/version from ${filename}, skipping..."
      continue
    fi

    app_img_canonical="${APP_REPO}/${app_name}:${version}-${TAG}"
    app_img_plain="${APP_REPO}/${app_name}:${version}"

    echo -e "\033[32mBuilding app image ${app_img_canonical} (${app_name}, version ${version})\033[m"

    podman build ${PODMAN_BUILD_EXTRA_FLAGS} \
      --build-context "common=${BASE_DIR}/common" \
      --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
      --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
      --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
      --label "io.flightctl.e2e.component=app" \
      -f "${containerfile}" \
      -t "${app_img_canonical}" \
      -t "${app_img_plain}" \
      "${BASE_DIR}"

    echo -e "\033[32mSuccessfully built ${app_img_canonical}\033[m"
  done
fi

echo -e "\033[32mBuild completed successfully.\033[m"

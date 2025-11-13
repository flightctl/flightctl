#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

TAG="${TAG:-latest}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
FLAVORS="${FLAVORS:-cs9 cs10}"
VARIANTS="${VARIANTS:-v2 v3 v4 v5 v6 v7 v8 v9 v10}"
JOBS="${JOBS:-$(command -v nproc >/dev/null && nproc || echo 1)}"
# Default variant parallelism: equals nproc unless overridden
VARIANT_JOBS="${VARIANT_JOBS:-$(command -v nproc >/dev/null && nproc || echo 1)}"
PODMAN_LOG_LEVEL="${PODMAN_LOG_LEVEL:-info}"
PODMAN_BUILD_EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS:-}"

SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

PODMAN_BUILD_FLAGS=(--jobs "${JOBS}" --log-level "${PODMAN_LOG_LEVEL}" --pull=missing --layers=true --network=host)

# CLI args
MODE="all"  # base-only | variants-only | all
while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-only)
      MODE="base-only"
      shift
      ;;
    --variants-only)
      MODE="variants-only"
      shift
      ;;
    --jobs)
      JOBS="$2"
      PODMAN_BUILD_FLAGS=(--jobs "${JOBS}" --log-level "${PODMAN_LOG_LEVEL}" --pull=missing --layers=true --network=host)
      shift 2
      ;;
    --variant-jobs)
      VARIANT_JOBS="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      echo "Usage: $0 [--base-only|--variants-only] [--jobs N] [--variant-jobs N]" >&2
      exit 1
      ;;
  esac
done

# Configure cache flags similar to legacy script (only in CI when REGISTRY is set)
if [ "${GITHUB_ACTIONS:-false}" = "true" ]; then
  REGISTRY="${REGISTRY:-}"
  REGISTRY_OWNER_TESTS="${REGISTRY_OWNER_TESTS:-flightctl-tests}"
  if [ -n "${REGISTRY}" ] && [ "${REGISTRY}" != "localhost" ]; then
    CACHE_FLAGS=("--cache-from=${REGISTRY}/${REGISTRY_OWNER_TESTS}/flightctl-device")
  else
    echo "Skipping remote cache-from in CI (no valid REGISTRY configured)"
    CACHE_FLAGS=()
  fi
else
  CACHE_FLAGS=()
fi

echo "Building flavors: ${FLAVORS}"
echo "TAG: ${TAG}"
echo "IMAGE_REPO: ${IMAGE_REPO}"

cd "$ROOT_DIR"

for flavor in ${FLAVORS}; do
  # shellcheck source=/dev/null
  source "${BASE_DIR}/flavors/${flavor}.env"
  : "${OS_ID:?OS_ID must be set in flavor env}"
  : "${BOOTC_BASE_IMAGE:?BOOTC_BASE_IMAGE must be set in flavor env}"

  base_img_canonical="${IMAGE_REPO}:base-${OS_ID}-${TAG}"
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

  if [ "${MODE}" = "all" ] || [ "${MODE}" = "base-only" ]; then
    echo -e "\033[32m[${OS_ID}] Building base ${base_img_canonical}\033[m"
    podman build "${CACHE_FLAGS[@]}" "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS} \
      --build-arg BOOTC_BASE_IMAGE="${BOOTC_BASE_IMAGE}" \
      --build-arg ENABLE_CRB="${ENABLE_CRB:-0}" \
      --build-arg EPEL_NEXT="${EPEL_NEXT:-1}" \
      --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
      --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
      --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
      -f "${BASE_DIR}/base/Containerfile" \
      -t "${base_img_canonical}" \
      "${ROOT_DIR}"
  fi

  if [ "${MODE}" = "all" ] || [ "${MODE}" = "variants-only" ]; then
    # Ensure the base image exists locally to avoid registry pulls
    if ! podman image exists "${base_img_canonical}"; then
      echo "[ERROR] Base image not found locally: ${base_img_canonical}" >&2
      echo "        Did you run --base-only successfully in this environment (same user/root)?" >&2
      exit 1
    fi
    echo -e "\033[32m[${OS_ID}] Building variants: ${variants_list}\033[m"
  # Join cache and build flags into strings for safe interpolation in xargs
  CACHE_FLAGS_JOINED=""
  if [ "${#CACHE_FLAGS[@]}" -gt 0 ]; then
    for f in "${CACHE_FLAGS[@]}"; do
      CACHE_FLAGS_JOINED+=" ${f}"
    done
  fi
  PODMAN_BUILD_FLAGS_JOINED=""
  if [ "${#PODMAN_BUILD_FLAGS[@]}" -gt 0 ]; then
    for f in "${PODMAN_BUILD_FLAGS[@]}"; do
      PODMAN_BUILD_FLAGS_JOINED+=" ${f}"
    done
  fi
  EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS:-}"

  export IMAGE_REPO TAG OS_ID BASE_DIR ROOT_DIR base_img_canonical CACHE_FLAGS_JOINED PODMAN_BUILD_FLAGS_JOINED EXTRA_FLAGS
  printf '%s\n' ${variants_list} | xargs -n1 -P "${VARIANT_JOBS}" -I {} bash -lc '\
    set -euo pipefail; \
    v_img_canonical="${IMAGE_REPO}:{}-'"${OS_ID}"'-'"${TAG}"'"; \
    prefix() { while IFS= read -r line; do printf "[%s] %s\n" "$v_img_canonical" "$line"; done; }; \
    printf "\033[32m['"${OS_ID}"'] Building variant %s\033[m\n" "$v_img_canonical" | prefix; \
    podman build'"${CACHE_FLAGS_JOINED}"' '"${PODMAN_BUILD_FLAGS_JOINED}"' --pull=never '"${EXTRA_FLAGS}"' \
      --build-arg BASE_IMAGE="'"${base_img_canonical}"'" \
      -f "'"${BASE_DIR}"'/variants/{}/Containerfile" \
      -t "$v_img_canonical" \
      "'"${ROOT_DIR}"'" 2>&1 | prefix'
  fi
done

echo -e "\033[32mBuild completed successfully.\033[m"



#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Default values
TAG="${TAG:-latest}"
SLEEP_APP_REPO="${SLEEP_APP_REPO:-quay.io/flightctl/sleep-app}"
PARALLEL_JOBS="${PARALLEL_JOBS:-4}"
VARIANTS="${VARIANTS:-v1 v2 v3}"

# Version build args
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --variants)
            VARIANTS="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --variants LIST   Space-separated list of variants to build (default: v1 v2 v3)"
            echo ""
            echo "Environment variables:"
            echo "  TAG               Image tag (default: latest)"
            echo "  SLEEP_APP_REPO    Image repository (default: quay.io/flightctl/sleep-app)"
            echo "  PARALLEL_JOBS     Number of parallel jobs for builds (default: 4)"
            echo "  SOURCE_GIT_TAG    Git tag for build args"
            echo "  SOURCE_GIT_TREE_STATE  Git tree state for build args"
            echo "  SOURCE_GIT_COMMIT Git commit for build args"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

echo "TAG: ${TAG}"
echo "SLEEP_APP_REPO: ${SLEEP_APP_REPO}"
echo "SOURCE_GIT_TAG: ${SOURCE_GIT_TAG}"
echo "SOURCE_GIT_COMMIT: ${SOURCE_GIT_COMMIT}"

cd "$PROJECT_ROOT"

build_sleep_app() {
    local variant="$1"
    local image_name="${SLEEP_APP_REPO}:${variant}-${TAG}"

    echo -e "\033[32mBuilding sleep app image ${image_name}\033[m"

    podman build \
        --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
        --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
        --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
        -f "${SCRIPT_DIR}/Containerfile-sleep-app-${variant}" \
        -t "${image_name}" \
        .

    echo -e "\033[32mSuccessfully built ${image_name}\033[m"
}

build_sleep_apps() {
    if ! [[ "$PARALLEL_JOBS" =~ ^[0-9]+$ ]] || [ "$PARALLEL_JOBS" -lt 1 ]; then
        echo -e "\033[31mError: PARALLEL_JOBS must be a positive integer, got: $PARALLEL_JOBS\033[m"
        PARALLEL_JOBS=1
    fi

    echo -e "\033[33mBuilding sleep app images in parallel (max $PARALLEL_JOBS jobs)...\033[m"
    echo -e "\033[33mVariants to build: ${VARIANTS}\033[m"

    local job_count=0
    for variant in $VARIANTS; do
        while [ $job_count -ge $PARALLEL_JOBS ]; do
            wait -n
            job_count=$((job_count - 1))
        done
        build_sleep_app "$variant" &
        job_count=$((job_count + 1))
    done
    wait

    echo -e "\033[32mAll sleep app images built successfully\033[m"
}

echo -e "\033[33mBuilding sleep app images...\033[m"
build_sleep_apps

echo -e "\033[32mBuild completed successfully!\033[m"
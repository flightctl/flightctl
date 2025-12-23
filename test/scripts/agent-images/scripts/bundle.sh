#!/usr/bin/env bash
set -euo pipefail

# Parse command line arguments
FILTER_ARGS=()
OUTPUT_PATH=""
IMAGE_PATTERN=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --filter)
      FILTER_ARGS+=("--filter" "$2")
      shift 2
      ;;
    --output-path)
      OUTPUT_PATH="$2"
      shift 2
      ;;
    --image-pattern)
      IMAGE_PATTERN="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      echo "Usage: $0 [--filter <filter>...] [--image-pattern <pattern>] --output-path <path>" >&2
      exit 1
      ;;
  esac
done

# Validate required arguments
if [ ${#FILTER_ARGS[@]} -eq 0 ] && [ -z "${IMAGE_PATTERN}" ]; then
  echo "Error: Either --filter arguments OR --image-pattern is required" >&2
  echo "Usage: $0 [--filter <filter>...] [--image-pattern <pattern>] --output-path <path>" >&2
  exit 1
fi

if [ -z "${OUTPUT_PATH}" ]; then
  echo "Error: --output-path is required" >&2
  echo "Usage: $0 [--filter <filter>...] [--image-pattern <pattern>] --output-path <path>" >&2
  exit 1
fi

# Create output directory if it doesn't exist
mkdir -p "$(dirname "${OUTPUT_PATH}")"

# Remove existing archive if it exists
rm -f "${OUTPUT_PATH}"

# Select images using filters and optional pattern
if [ -n "${IMAGE_PATTERN}" ]; then
  # Use programmatic filtering with grep pattern
  mapfile -t refs < <(
    podman images --format '{{.Repository}}:{{.Tag}}' "${FILTER_ARGS[@]}" | grep "^${IMAGE_PATTERN}$" || true
  )
else
  # Use only podman filters
  mapfile -t refs < <(
    podman images --format '{{.Repository}}:{{.Tag}}' "${FILTER_ARGS[@]}" || true
  )
fi

if [ "${#refs[@]}" -eq 0 ]; then
  echo "No images found with the specified filters:" >&2
  printf '  %s\n' "${FILTER_ARGS[@]}" >&2
  exit 1
fi

echo -e "\033[32mBundling ${#refs[@]} images:\033[m"
for ref in "${refs[@]}"; do
  printf '\t- %s\n' "${ref}"
done

echo -e "\033[32mSaving bundle to ${OUTPUT_PATH}\033[m"
podman save --multi-image-archive -o "${OUTPUT_PATH}" "${refs[@]}"
echo -e "\033[32mBundle created: ${OUTPUT_PATH}\033[m"



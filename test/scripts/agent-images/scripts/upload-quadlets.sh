#!/usr/bin/env bash
set -euo pipefail

# Upload quadlet artifacts to a registry.
# Usage: ./upload-quadlets.sh [--registry-endpoint host:port]
#
# Quadlets are discovered from ../quadlets/ directory.
# Apps (containing .container, .pod, .volume files) are bundled as tarballs.
# Volumes are pushed as raw files.
# If REGISTRY_ENDPOINT is not provided, it will be calculated using registry_address()
#
# Uses podman artifact (5.4+) if available, otherwise falls back to oras.

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/../../functions"

ARG_ENDPOINT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --registry-endpoint) ARG_ENDPOINT="$2"; shift 2 ;;
    -*)
      echo "unknown arg: $1"; exit 2 ;;
    *)
      echo "unknown arg: $1"; exit 2 ;;
  esac
done

REGISTRY_ENDPOINT="${REGISTRY_ENDPOINT:-$ARG_ENDPOINT}"
if [ -z "${REGISTRY_ENDPOINT}" ]; then
  REGISTRY_ENDPOINT=$(registry_address)
  echo "Using calculated registry address: ${REGISTRY_ENDPOINT}"
fi

check_registry "${REGISTRY_ENDPOINT}"

# Determine which tool to use for pushing artifacts
USE_PODMAN_ARTIFACT=false
USE_ORAS=false

# Check podman version - artifact subcommand requires 5.4+
PODMAN_VERSION=$(podman --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+' | head -1 || echo "0.0")
PODMAN_MAJOR=$(echo "${PODMAN_VERSION}" | cut -d. -f1)
PODMAN_MINOR=$(echo "${PODMAN_VERSION}" | cut -d. -f2)

if [ "${PODMAN_MAJOR}" -gt 5 ] 2>/dev/null || { [ "${PODMAN_MAJOR}" -eq 5 ] && [ "${PODMAN_MINOR}" -ge 4 ]; } 2>/dev/null; then
  USE_PODMAN_ARTIFACT=true
  echo "Using podman artifact for OCI artifacts (podman ${PODMAN_VERSION})"
elif command -v oras &>/dev/null; then
  USE_ORAS=true
  echo "Using oras for OCI artifacts"
else
  echo "ERROR: Neither 'podman artifact' (podman 5.4+) nor 'oras' is available"
  exit 1
fi

push_artifact() {
  local artifact_ref="$1"
  local media_type="$2"
  shift 2
  local files=("$@")

  if [ "${USE_PODMAN_ARTIFACT}" = "true" ]; then
    podman artifact rm "${artifact_ref}" 2>/dev/null || true
    podman artifact add "${artifact_ref}" "${files[@]}"
    podman artifact push --tls-verify=false "${artifact_ref}"
    podman artifact rm "${artifact_ref}"
  else
    # oras needs media type annotations and relative paths
    # cd to directory containing files so titles are just filenames
    local oras_files=()
    local pushdir
    pushdir=$(dirname "${files[0]}")
    for f in "${files[@]}"; do
      oras_files+=("$(basename "${f}"):${media_type}")
    done
    (cd "${pushdir}" && oras push --insecure "${artifact_ref}" "${oras_files[@]}")
  fi
}

quadlets_dir="${SCRIPT_DIR}/../quadlets"
artifact_count=0
tmpdir=$(mktemp -d)
trap "rm -rf ${tmpdir}" EXIT

# Push quadlet apps as tarballs (contain .container, .pod, .volume files)
for artifact_dir in "${quadlets_dir}"/apps/*/; do
  if [ -d "${artifact_dir}" ]; then
    artifact_name=$(basename "${artifact_dir}")
    tarball="${tmpdir}/${artifact_name}.tar.gz"

    echo "Bundling quadlet app: ${artifact_name}"
    tar -czf "${tarball}" -C "${artifact_dir}" .

    artifact_ref="${REGISTRY_ENDPOINT}/flightctl/quadlets/${artifact_name}:latest"
    echo "Pushing ${artifact_name} to ${artifact_ref}"

    push_artifact "${artifact_ref}" "application/x-gzip" "${tarball}"

    artifact_count=$((artifact_count + 1))
  fi
done

# Push volume data as raw files
for artifact_dir in "${quadlets_dir}"/volumes/*/; do
  if [ -d "${artifact_dir}" ]; then
    artifact_name=$(basename "${artifact_dir}")
    artifact_ref="${REGISTRY_ENDPOINT}/flightctl/quadlets/${artifact_name}:latest"

    echo "Pushing volume artifact: ${artifact_name} to ${artifact_ref}"

    # Collect files
    files=()
    for f in "${artifact_dir}"/*; do
      [ -f "$f" ] && files+=("${f}")
    done

    if [ ${#files[@]} -eq 0 ]; then
      echo "Warning: No files found in ${artifact_dir}, skipping"
      continue
    fi

    push_artifact "${artifact_ref}" "text/plain" "${files[@]}"

    artifact_count=$((artifact_count + 1))
  fi
done

echo "Done. Pushed ${artifact_count} quadlet artifact(s) to ${REGISTRY_ENDPOINT}"

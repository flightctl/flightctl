#!/usr/bin/env bash
set -euo pipefail

# Upload Helm charts to an OCI registry.
# Usage: ./upload-charts.sh [--registry-endpoint host:port]
#
# Charts are discovered from ../charts/ directory.
# If REGISTRY_ENDPOINT is not provided, it will be calculated using registry_address()

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

charts_dir="${SCRIPT_DIR}/../charts"
chart_count=0
tmpdir=$(mktemp -d)
trap "rm -rf ${tmpdir}" EXIT

declare -A TEST_APP_VERSIONS=(
  ["0.1.0"]="hello v1"
  ["0.2.0"]="hello v2"
)

for chart_dir in "${charts_dir}"/*/; do
  if [ -f "${chart_dir}/Chart.yaml" ]; then
    chart_name=$(basename "${chart_dir}")

    if [[ "${chart_name}" == "test-app" ]]; then
      for version in "${!TEST_APP_VERSIONS[@]}"; do
        message="${TEST_APP_VERSIONS[$version]}"
        work_dir="${tmpdir}/${chart_name}-${version}"
        cp -r "${chart_dir}" "${work_dir}"

        sed -i "s/^version:.*/version: ${version}/" "${work_dir}/Chart.yaml"
        sed -i "s/^message:.*/message: \"${message}\"/" "${work_dir}/values.yaml"

        echo "Packaging chart: ${chart_name} (version: ${version}, message: ${message})"
        pkg_file=$(helm package "${work_dir}" --destination /tmp | awk '{print $NF}')

        echo "Pushing ${chart_name}:${version} to ${REGISTRY_ENDPOINT}/flightctl/charts/${chart_name}:${version}"
        helm push "${pkg_file}" "oci://${REGISTRY_ENDPOINT}/flightctl/charts" --insecure-skip-tls-verify

        rm -f "${pkg_file}"
        chart_count=$((chart_count + 1))
      done
    else
      chart_version=$(grep '^version:' "${chart_dir}/Chart.yaml" | awk '{print $2}')

      echo "Packaging chart: ${chart_name} (version: ${chart_version})"
      pkg_file=$(helm package "${chart_dir}" --destination /tmp | awk '{print $NF}')

      echo "Pushing ${chart_name}:${chart_version} to ${REGISTRY_ENDPOINT}/flightctl/charts/${chart_name}:${chart_version}"
      helm push "${pkg_file}" "oci://${REGISTRY_ENDPOINT}/flightctl/charts" --insecure-skip-tls-verify

      rm -f "${pkg_file}"
      chart_count=$((chart_count + 1))
    fi
  fi
done

echo "Done. Pushed ${chart_count} chart(s) to ${REGISTRY_ENDPOINT}"

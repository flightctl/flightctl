#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/../functions"

VARIANTS=(base v2 v3 v4 v5 v6 v8 v9 v10)

REGISTRY_ADDRESS=${REGISTRY_ADDRESS:-$(registry_address)}

build_finalize_image() {
  local variant="$1"
  local seed_ref="localhost:5000/flightctl-device-seed:${variant}"
  local final_local="localhost:5000/flightctl-device:${variant}"
  local final_remote="${REGISTRY_ADDRESS}/flightctl-device:${variant}"

  if ! podman image exists "${seed_ref}"; then
    echo "Seed image ${seed_ref} not found. Load it or build seed first." >&2
    return 1
  fi

  podman build \
    --build-arg BASE_IMAGE="${seed_ref}" \
    --build-arg REGISTRY_ADDRESS="${REGISTRY_ADDRESS}" \
    -f "${SCRIPT_DIR}/Containerfile-agent-config.local" \
    -t "${final_local}" .

  podman tag "${final_local}" "${final_remote}"
  podman push "${final_remote}"
}

# Build finalize images for all variants
for v in "${VARIANTS[@]}"; do
  build_finalize_image "$v"
done

# Finalize qcow2 by injecting config into the seed disk when possible
SEED_QCOW="bin/output/qcow2/disk-seed.qcow2"
FINAL_QCOW="bin/output/qcow2/disk.qcow2"
if [[ -f "${SEED_QCOW}" ]]; then
  cp -f "${SEED_QCOW}" "${FINAL_QCOW}"
  if command -v virt-customize >/dev/null 2>&1; then
    echo "Injecting agent config and certs into qcow2..."
    # Prepare registries.conf snippet for qcow2
    tmp_reg_conf=$(mktemp)
    cat >"${tmp_reg_conf}" <<EOF
[[registry]]
location = "${REGISTRY_ADDRESS}"
insecure = true
EOF
    virt-customize -a "${FINAL_QCOW}" \
      --mkdir /etc/flightctl/certs \
      --upload bin/agent/etc/flightctl/config.yaml:/etc/flightctl/config.yaml \
      --copy-in bin/agent/etc/flightctl/certs/:/etc/flightctl/certs/ \
      --mkdir /etc/containers/registries.conf.d \
      --upload "${tmp_reg_conf}":/etc/containers/registries.conf.d/custom-registry.conf
    rm -f "${tmp_reg_conf}"
  else
    echo "virt-customize not found; leaving seed qcow2 as-is at ${FINAL_QCOW}" >&2
  fi
fi



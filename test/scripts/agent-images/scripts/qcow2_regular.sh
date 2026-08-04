#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
OS_ID="${OS_ID:?OS_ID is required}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"

BASE_CLOUD_IMAGE_URL="${BASE_CLOUD_IMAGE_URL:-https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2}"
CLOUD_IMAGE_CACHE_DIR="${CLOUD_IMAGE_CACHE_DIR:-${ROOT_DIR}/bin/cloud-images}"
BASE_CLOUD_IMAGE_PATH="${CLOUD_IMAGE_CACHE_DIR}/$(basename "${BASE_CLOUD_IMAGE_URL}")"

RPM_DIR="${RPM_DIR:-rpm}"
RPM_COPR_REPO="${RPM_COPR_REPO:-}"
RPM_COPR_PACKAGE="${RPM_COPR_PACKAGE:-}"
TMP_CLOUD_IMAGE_PATH=""
RPM_SOURCE_DIR=""

PKG_CACHE_DIR=""
cleanup() {
  if [ -n "${TMP_CLOUD_IMAGE_PATH}" ] && [ -f "${TMP_CLOUD_IMAGE_PATH}" ]; then
    rm -f "${TMP_CLOUD_IMAGE_PATH}"
  fi
  if [ -n "${PKG_CACHE_DIR}" ] && [ -d "${PKG_CACHE_DIR}" ]; then
    rm -rf "${PKG_CACHE_DIR}"
  fi
}

# Fail early with a clear message when a required host-side tool is missing.
require_command() {
  local command_name="$1"
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Missing required command: ${command_name}" >&2
    exit 1
  fi
}

# Accept only owner/project COPR references before embedding them in guest commands.
validate_copr_repo() {
  local copr_repo="$1"
  if [[ ! "${copr_repo}" =~ ^@?[A-Za-z0-9._+-]+/[A-Za-z0-9._+-]+$ ]]; then
    echo "Invalid RPM_COPR_REPO: ${copr_repo}" >&2
    exit 1
  fi
}

# Accept only package-name characters before embedding them in guest commands.
validate_copr_package() {
  local copr_package="$1"
  if [[ ! "${copr_package}" =~ ^[A-Za-z0-9._:+-]+$ ]]; then
    echo "Invalid RPM_COPR_PACKAGE: ${copr_package}" >&2
    exit 1
  fi
}

# Keep local RPM lookups confined to a simple directory name beneath bin/.
validate_rpm_dir() {
  local rpm_dir="$1"
  if [[ ! "${rpm_dir}" =~ ^[A-Za-z0-9._+-]+$ ]] || [[ "${rpm_dir}" == *"/"* ]] || [[ "${rpm_dir}" == *".."* ]]; then
    echo "Invalid RPM_DIR: ${rpm_dir}" >&2
    exit 1
  fi
}

# Resolve the caller-provided RPM directory under bin/ and reject path escape.
resolve_rpm_source_dir() {
  local rpm_dir="$1"
  local bin_root="${ROOT_DIR}/bin"
  local resolved_path

  resolved_path="$(realpath -m "${bin_root}/${rpm_dir}")"
  if [[ "${resolved_path}" != "${bin_root}"/* ]]; then
    echo "RPM_DIR resolves outside ${bin_root}: ${rpm_dir}" >&2
    exit 1
  fi

  if [ ! -d "${resolved_path}" ]; then
    echo "Local RPM directory not found: ${resolved_path}" >&2
    exit 1
  fi

  RPM_SOURCE_DIR="${resolved_path}"
}

# Keep qcow output under bin/output even when callers override OUTPUT_DIR.
resolve_output_dir() {
  local output_dir="$1"
  local output_root="${ROOT_DIR}/bin/output"
  local resolved_path

  resolved_path="$(realpath -m "${output_dir}")"
  if [[ "${resolved_path}" != "${output_root}"/* ]]; then
    echo "OUTPUT_DIR must resolve beneath ${output_root}: ${output_dir}" >&2
    exit 1
  fi

  OUTPUT_DIR="${resolved_path}"
}

if [ "${OS_ID}" != "cs9-regular" ]; then
  echo "qcow2_regular.sh currently supports only cs9-regular, got ${OS_ID}" >&2
  exit 1
fi

require_command curl
require_command qemu-img
require_command virt-customize
require_command podman

resolve_output_dir "${OUTPUT_DIR}"
OUTPUT_QCOW_DIR="${OUTPUT_DIR}/qcow2"
OUTPUT_QCOW_PATH="${OUTPUT_QCOW_DIR}/disk.qcow2"

mkdir -p "${CLOUD_IMAGE_CACHE_DIR}" "${OUTPUT_QCOW_DIR}"
trap cleanup EXIT

if [ ! -f "${BASE_CLOUD_IMAGE_PATH}" ]; then
  echo "Downloading CentOS Stream 9 cloud image from ${BASE_CLOUD_IMAGE_URL}"
  TMP_CLOUD_IMAGE_PATH="$(mktemp "${CLOUD_IMAGE_CACHE_DIR}/$(basename "${BASE_CLOUD_IMAGE_PATH}").tmp.XXXXXX")"
  curl --fail --location --retry 3 --output "${TMP_CLOUD_IMAGE_PATH}" "${BASE_CLOUD_IMAGE_URL}"
  mv "${TMP_CLOUD_IMAGE_PATH}" "${BASE_CLOUD_IMAGE_PATH}"
  TMP_CLOUD_IMAGE_PATH=""
fi

echo "Preparing package-mode qcow2 at ${OUTPUT_QCOW_PATH}"
qemu-img convert -O qcow2 "${BASE_CLOUD_IMAGE_PATH}" "${OUTPUT_QCOW_PATH}"
qemu-img resize "${OUTPUT_QCOW_PATH}" +5G

# Download all required packages on the host using a CentOS 9 container so
# virt-customize can install them offline (--no-network).  This sidesteps the
# libguestfs/passt networking issue: on QEMU >= 7.2 libguestfs uses passt for
# appliance networking, which needs user-namespace creation — denied on
# GitHub-hosted runners.
echo "Downloading required packages for offline guest installation"
PKG_CACHE_DIR=$(mktemp -d)

CONTAINER_CMD='
  set -euo pipefail
  dnf install -y epel-release epel-next-release
  dnf download --resolve --destdir=/output \
    epel-release epel-next-release \
    cloud-init dnf-plugins-core firewalld openssh-server \
    podman podman-compose python-dotenv sudo
'

if [ -n "${RPM_COPR_REPO}" ]; then
  package_name="${RPM_COPR_PACKAGE:-flightctl-agent}"
  validate_copr_repo "${RPM_COPR_REPO}"
  validate_copr_package "${package_name}"
  CONTAINER_CMD+="
    dnf copr -y enable ${RPM_COPR_REPO}
    dnf download --resolve --destdir=/output ${package_name}
  "
else
  validate_rpm_dir "${RPM_DIR}"
  resolve_rpm_source_dir "${RPM_DIR}"
fi

podman run --rm -v "${PKG_CACHE_DIR}:/output:Z" quay.io/centos/centos:stream9 \
  bash -c "${CONTAINER_CMD}"

if [ -z "${RPM_COPR_REPO}" ]; then
  cp "${RPM_SOURCE_DIR}"/flightctl-agent-*.rpm "${RPM_SOURCE_DIR}"/flightctl-selinux-*.rpm \
    "${PKG_CACHE_DIR}/"
fi

VIRT_CUSTOMIZE_ARGS=(
  -a "${OUTPUT_QCOW_PATH}"
  --no-network
  --copy-in "${PKG_CACHE_DIR}:/tmp/pkg-cache"
  --run-command "dnf install -y /tmp/pkg-cache/*.rpm"
  --run-command "rm -rf /tmp/pkg-cache"
  --run-command "systemctl enable firewalld.service"
  --run-command "systemctl enable podman.service"
  --run-command "systemctl enable sshd.service"
  --run-command "id -u user >/dev/null 2>&1 || useradd -ms /bin/bash user"
  --run-command "echo 'user:user' | chpasswd"
  --run-command "echo 'user ALL=(ALL) NOPASSWD: ALL' >> /etc/sudoers"
  --run-command "mkdir -p /usr/lib/flightctl/custom-info.d"
  --run-command "printf '#!/bin/bash\necho \"my site\"\n' > /usr/lib/flightctl/custom-info.d/siteName"
  --run-command "printf '#!/bin/bash\necho \"\"\n' > /usr/lib/flightctl/custom-info.d/emptyValue"
  --run-command "printf '#!/bin/bash\necho \"no-show\"\n' > /usr/lib/flightctl/custom-info.d/keyNotShown"
  --run-command "chmod 755 /usr/lib/flightctl/custom-info.d/*"
  --run-command "systemctl enable flightctl-agent.service"
  --run-command "dnf clean all"
  --selinux-relabel
)

echo "Customizing regular package-mode qcow2"
sudo env LIBGUESTFS_BACKEND=direct virt-customize "${VIRT_CUSTOMIZE_ARGS[@]}"

sudo chown "${USER}:$(id -gn "${USER}")" "${OUTPUT_QCOW_PATH}"

echo "qcow2 image created at ${OUTPUT_QCOW_PATH}"
ls -lh "${OUTPUT_QCOW_DIR}"

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
GUEST_MOUNT=""

cleanup() {
  if [ -n "${GUEST_MOUNT}" ] && mountpoint -q "${GUEST_MOUNT}" 2>/dev/null; then
    sudo guestunmount "${GUEST_MOUNT}" || sudo umount -l "${GUEST_MOUNT}" || true
  fi
  if [ -n "${GUEST_MOUNT}" ] && [ -d "${GUEST_MOUNT}" ]; then
    rmdir "${GUEST_MOUNT}" 2>/dev/null || true
  fi
  if [ -n "${TMP_CLOUD_IMAGE_PATH}" ] && [ -f "${TMP_CLOUD_IMAGE_PATH}" ]; then
    rm -f "${TMP_CLOUD_IMAGE_PATH}"
  fi
}

require_command() {
  local command_name="$1"
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Missing required command: ${command_name}" >&2
    exit 1
  fi
}

validate_copr_repo() {
  local copr_repo="$1"
  if [[ ! "${copr_repo}" =~ ^@?[A-Za-z0-9._+-]+/[A-Za-z0-9._+-]+$ ]]; then
    echo "Invalid RPM_COPR_REPO: ${copr_repo}" >&2
    exit 1
  fi
}

validate_copr_package() {
  local copr_package="$1"
  if [[ ! "${copr_package}" =~ ^[A-Za-z0-9._:+-]+$ ]]; then
    echo "Invalid RPM_COPR_PACKAGE: ${copr_package}" >&2
    exit 1
  fi
}

validate_rpm_dir() {
  local rpm_dir="$1"
  if [[ ! "${rpm_dir}" =~ ^[A-Za-z0-9._+-]+$ ]] || [[ "${rpm_dir}" == *"/"* ]] || [[ "${rpm_dir}" == *".."* ]]; then
    echo "Invalid RPM_DIR: ${rpm_dir}" >&2
    exit 1
  fi
}

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

# Install packages into a mounted cloud-image root using CentOS Stream 9's dnf
# (host runners are Ubuntu and have no native dnf). Networking stays on the
# container/host — never inside a libguestfs appliance.
install_into_root() {
  local install_root="$1"
  local container_cmd

  container_cmd=$(cat <<'EOS'
set -euo pipefail
root=/installroot

dnf --installroot="${root}" -y install epel-release epel-next-release
# dnf resolves file:/// GPG key URLs against the running system, not installroot.
mkdir -p /etc/pki/rpm-gpg
cp -f "${root}"/etc/pki/rpm-gpg/RPM-GPG-KEY-* /etc/pki/rpm-gpg/

dnf --installroot="${root}" -y install \
  cloud-init \
  dnf-plugins-core \
  firewalld \
  openssh-server \
  podman \
  podman-compose \
  python-dotenv \
  sudo

if [ -n "${RPM_COPR_REPO}" ]; then
  dnf --installroot="${root}" copr -y enable "${RPM_COPR_REPO}"
  dnf --installroot="${root}" -y install "${RPM_COPR_PACKAGE}"
else
  dnf --installroot="${root}" -y install /rpms/flightctl-agent-*.rpm /rpms/flightctl-selinux-*.rpm
fi

systemctl --root="${root}" enable firewalld.service
systemctl --root="${root}" enable podman.service
systemctl --root="${root}" enable sshd.service
systemctl --root="${root}" enable flightctl-agent.service

if ! grep -q '^user:' "${root}/etc/passwd"; then
  chroot "${root}" useradd -ms /bin/bash user
fi
echo 'user:user' | chroot "${root}" chpasswd
if ! grep -q '^user ALL=(ALL) NOPASSWD: ALL' "${root}/etc/sudoers"; then
  echo 'user ALL=(ALL) NOPASSWD: ALL' >> "${root}/etc/sudoers"
fi

mkdir -p "${root}/usr/lib/flightctl/custom-info.d"
printf '#!/bin/bash\necho "my site"\n' > "${root}/usr/lib/flightctl/custom-info.d/siteName"
printf '#!/bin/bash\necho ""\n' > "${root}/usr/lib/flightctl/custom-info.d/emptyValue"
printf '#!/bin/bash\necho "no-show"\n' > "${root}/usr/lib/flightctl/custom-info.d/keyNotShown"
chmod 755 "${root}/usr/lib/flightctl/custom-info.d" "${root}/usr/lib/flightctl/custom-info.d/"*

dnf --installroot="${root}" clean all
EOS
)

  local -a podman_args=(
    run --rm
    -e "RPM_COPR_REPO=${RPM_COPR_REPO}"
    -e "RPM_COPR_PACKAGE=${RPM_COPR_PACKAGE:-flightctl-agent}"
    -v "${install_root}:/installroot:Z"
  )

  if [ -z "${RPM_COPR_REPO}" ]; then
    podman_args+=(-v "${RPM_SOURCE_DIR}:/rpms:ro,Z")
  fi

  # Rootful podman matches the BIB path and avoids rootless crun issues on GHA.
  sudo podman "${podman_args[@]}" \
    quay.io/centos/centos:stream9 \
    bash -c "${container_cmd}"
}

if [ "${OS_ID}" != "cs9-regular" ]; then
  echo "qcow2_regular.sh currently supports only cs9-regular, got ${OS_ID}" >&2
  exit 1
fi

require_command curl
require_command qemu-img
require_command guestmount
require_command guestunmount
require_command podman
require_command virt-customize
require_command mountpoint

resolve_output_dir "${OUTPUT_DIR}"
OUTPUT_QCOW_DIR="${OUTPUT_DIR}/qcow2"
OUTPUT_QCOW_PATH="${OUTPUT_QCOW_DIR}/disk.qcow2"

mkdir -p "${CLOUD_IMAGE_CACHE_DIR}" "${OUTPUT_QCOW_DIR}"
trap cleanup EXIT

if [ -n "${RPM_COPR_REPO}" ]; then
  validate_copr_repo "${RPM_COPR_REPO}"
  validate_copr_package "${RPM_COPR_PACKAGE:-flightctl-agent}"
else
  validate_rpm_dir "${RPM_DIR}"
  resolve_rpm_source_dir "${RPM_DIR}"
fi

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

# Mount the bootable cloud disk on the host. guestmount uses a libguestfs
# appliance for filesystem access only (no guest networking / guest dnf).
GUEST_MOUNT="$(mktemp -d "${TMPDIR:-/tmp}/cs9-regular-guest.XXXXXX")"
echo "Mounting qcow2 at ${GUEST_MOUNT}"
sudo env LIBGUESTFS_BACKEND=direct guestmount -a "${OUTPUT_QCOW_PATH}" -i --rw "${GUEST_MOUNT}"

echo "Installing packages into qcow2 via host-side dnf --installroot"
install_into_root "${GUEST_MOUNT}"

echo "Unmounting qcow2"
sudo guestunmount "${GUEST_MOUNT}"
rmdir "${GUEST_MOUNT}" 2>/dev/null || true
GUEST_MOUNT=""

# Relabel without enabling appliance networking.
echo "SELinux relabel"
sudo env LIBGUESTFS_BACKEND=direct virt-customize -a "${OUTPUT_QCOW_PATH}" --no-network --selinux-relabel

sudo chown "${USER}:$(id -gn "${USER}")" "${OUTPUT_QCOW_PATH}"

echo "qcow2 image created at ${OUTPUT_QCOW_PATH}"
ls -lh "${OUTPUT_QCOW_DIR}"

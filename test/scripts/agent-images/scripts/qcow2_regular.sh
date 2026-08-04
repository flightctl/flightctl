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
POST_CONFIG_SCRIPT=""

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
  if [ -n "${POST_CONFIG_SCRIPT}" ] && [ -f "${POST_CONFIG_SCRIPT}" ]; then
    rm -f "${POST_CONFIG_SCRIPT}"
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
#
# Flightctl RPMs are installed with tsflags=noscripts: agent/selinux %post
# (semodule, useradd, linger) is unreliable under guestmount FUSE. That work
# runs later in virt-customize after the disk is unmounted.
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
  dnf --installroot="${root}" --setopt=tsflags=noscripts -y install "${RPM_COPR_PACKAGE}"
else
  shopt -s nullglob
  rpms=(/rpms/flightctl-agent-*.rpm /rpms/flightctl-selinux-*.rpm)
  shopt -u nullglob
  if [ "${#rpms[@]}" -eq 0 ]; then
    echo "No flightctl-agent/flightctl-selinux RPMs found in /rpms" >&2
    exit 1
  fi
  dnf --installroot="${root}" --setopt=tsflags=noscripts -y install "${rpms[@]}"
fi

# centos:stream9 has no systemctl; enable units via the mounted root (has systemd).
chroot "${root}" systemctl enable firewalld.service
chroot "${root}" systemctl enable podman.service
chroot "${root}" systemctl enable sshd.service
chroot "${root}" systemctl enable flightctl-agent.service

dnf --installroot="${root}" clean all
EOS
)

  local -a podman_args=(
    run --rm
    -e "RPM_COPR_REPO=${RPM_COPR_REPO}"
    -e "RPM_COPR_PACKAGE=${RPM_COPR_PACKAGE:-flightctl-agent}"
    # No :Z — guestmount is FUSE; SELinux relabel of the mount is useless on
    # Ubuntu runners and can interfere with subsequent creates.
    -v "${install_root}:/installroot:rw"
  )

  if [ -z "${RPM_COPR_REPO}" ]; then
    podman_args+=(-v "${RPM_SOURCE_DIR}:/rpms:ro,Z")
  fi

  # Rootful podman matches the BIB path and avoids rootless crun issues on GHA.
  # Feed the script on stdin (static heredoc) rather than bash -c with a variable.
  printf '%s' "${container_cmd}" | sudo podman "${podman_args[@]}" -i \
    quay.io/centos/centos:stream9 \
    bash -s
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
sudo guestunmount --retry=5 "${GUEST_MOUNT}" || sudo umount -l "${GUEST_MOUNT}"
while mountpoint -q "${GUEST_MOUNT}" 2>/dev/null; do
  sleep 1
done
rmdir "${GUEST_MOUNT}" 2>/dev/null || true
GUEST_MOUNT=""

# Finish flightctl %post work and e2e users in the libguestfs appliance (not via FUSE).
# Load the SELinux module before --selinux-relabel so flightctl_agent_* types exist.
echo "Configuring SELinux module, e2e users, and SELinux relabel"
POST_CONFIG_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/cs9-regular-post.XXXXXX.sh")"
cat >"${POST_CONFIG_SCRIPT}" <<'EOF'
#!/bin/bash
set -euo pipefail

flightctl_pp=/usr/share/selinux/packages/targeted/flightctl_agent.pp.bz2
if [ ! -f "${flightctl_pp}" ]; then
  echo "Missing flightctl SELinux policy package: ${flightctl_pp}" >&2
  exit 1
fi
if ! semodule -l | grep -q '^flightctl_agent'; then
  echo "Loading flightctl_agent SELinux module"
  semodule -s targeted -i "${flightctl_pp}"
fi
if ! semodule -l | grep -q '^flightctl_agent'; then
  echo "flightctl_agent SELinux module is not loaded after semodule -i" >&2
  exit 1
fi
echo "flightctl_agent SELinux module is loaded"

mkdir -p /var/lib/flightctl
chmod 0755 /var/lib/flightctl
id -u flightctl >/dev/null 2>&1 || useradd --create-home --user-group flightctl
flightctl_home="$(getent passwd flightctl | cut -d: -f6)"
if [ -z "${flightctl_home}" ]; then
  echo "flightctl user has no home directory in passwd" >&2
  exit 1
fi
mkdir -p \
  "${flightctl_home}/.config/containers/systemd" \
  "${flightctl_home}/.config/systemd/user" \
  "${flightctl_home}/.local"
chown -R flightctl:flightctl "${flightctl_home}/.config" "${flightctl_home}/.local"
mkdir -p /var/lib/systemd/linger
touch /var/lib/systemd/linger/flightctl

id -u user >/dev/null 2>&1 || useradd -ms /bin/bash user
echo 'user:user' | chpasswd
if ! grep -q '^user ALL=(ALL) NOPASSWD: ALL' /etc/sudoers; then
  echo 'user ALL=(ALL) NOPASSWD: ALL' >> /etc/sudoers
fi
mkdir -p /usr/lib/flightctl/custom-info.d
printf '#!/bin/bash\necho "my site"\n' > /usr/lib/flightctl/custom-info.d/siteName
printf '#!/bin/bash\necho ""\n' > /usr/lib/flightctl/custom-info.d/emptyValue
printf '#!/bin/bash\necho "no-show"\n' > /usr/lib/flightctl/custom-info.d/keyNotShown
chmod 755 /usr/lib/flightctl/custom-info.d /usr/lib/flightctl/custom-info.d/*
EOF
chmod 755 "${POST_CONFIG_SCRIPT}"
sudo env LIBGUESTFS_BACKEND=direct virt-customize -a "${OUTPUT_QCOW_PATH}" --no-network \
  --run "${POST_CONFIG_SCRIPT}" \
  --selinux-relabel
rm -f "${POST_CONFIG_SCRIPT}"
POST_CONFIG_SCRIPT=""

sudo chown "${USER}:$(id -gn "${USER}")" "${OUTPUT_QCOW_PATH}"

echo "qcow2 image created at ${OUTPUT_QCOW_PATH}"
ls -lh "${OUTPUT_QCOW_DIR}"

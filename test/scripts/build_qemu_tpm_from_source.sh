#!/bin/bash
set -e

# Builds upstream QEMU from source with TPM passthrough enabled.
# Installs to /usr/local/qemu-tpm to avoid conflicting with the system qemu-kvm.
#
# To remove: sudo rm -rf /usr/local/qemu-tpm

INSTALL_PREFIX="/usr/local/qemu-tpm"
QEMU_VERSION="${QEMU_VERSION:-v10.1.0}"
BUILD_DIR="${BUILD_DIR:-/tmp/qemu-tpm-build}"

echo "Building QEMU ${QEMU_VERSION} with TPM passthrough support"
echo "Install prefix: ${INSTALL_PREFIX}"
echo "Build directory: ${BUILD_DIR}"

echo "Installing build dependencies..."
sudo dnf install -y epel-release
sudo dnf config-manager --set-enabled crb
sudo dnf install -y \
    gcc make ninja-build meson \
    glib2-devel pixman-devel \
    libaio-devel libcap-ng-devel libattr-devel \
    python3 flex bison \
    libslirp-devel \
    git

mkdir -p "${BUILD_DIR}"
cd "${BUILD_DIR}"

if [ ! -d "qemu" ]; then
    echo "Cloning QEMU..."
    git clone https://gitlab.com/qemu-project/qemu.git
fi

cd qemu
git fetch --tags
git checkout "${QEMU_VERSION}"

echo "Configuring QEMU..."
./configure \
    --target-list=x86_64-softmmu \
    --enable-tpm \
    --enable-kvm \
    --enable-slirp \
    --prefix="${INSTALL_PREFIX}"

echo "Building QEMU..."
make -j"$(nproc)"

echo "Installing to ${INSTALL_PREFIX}..."
sudo make install

echo ""
echo "Verifying TPM passthrough support..."
TPM_BACKENDS=$("${INSTALL_PREFIX}/bin/qemu-system-x86_64" -tpmdev help 2>&1)
if echo "${TPM_BACKENDS}" | grep -q "passthrough"; then
    echo "TPM passthrough support confirmed:"
    echo "${TPM_BACKENDS}"
else
    echo "ERROR: TPM passthrough not found in built QEMU"
    echo "${TPM_BACKENDS}"
    exit 1
fi

echo ""
echo "Build complete. Custom QEMU installed at: ${INSTALL_PREFIX}"
echo "Emulator binary: ${INSTALL_PREFIX}/bin/qemu-system-x86_64"
echo ""
echo "To use with libvirt, set the emulator path in domain XML:"
echo "  <emulator>${INSTALL_PREFIX}/bin/qemu-system-x86_64</emulator>"
echo ""
echo "To remove: sudo rm -rf ${INSTALL_PREFIX}"

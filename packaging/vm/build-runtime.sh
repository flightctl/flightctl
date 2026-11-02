#!/usr/bin/env bash
set -euo pipefail

# Build on the same Enterprise Linux userspace as the worker image. Distro QEMU
# builds can omit 9p, so these features are explicit and checked at build time.
install_root=${1:?usage: build-runtime.sh INSTALL_ROOT}
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
cd "$build_dir"

fetch() {
    local url=$1 checksum=$2 archive=$3
    curl --fail --location --connect-timeout 20 --max-time 300 --retry 3 --output "$archive" "$url"
    printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --strict
    tar -xf "$archive"
}

fetch https://download.qemu.org/qemu-10.2.4.tar.xz \
    821b545b92f165e57dddccac5077d76d4d436a226595b8813ad59306bbfd0746 qemu.tar.xz
fetch https://gitlab.freedesktop.org/slirp/libslirp/-/archive/v4.9.1/libslirp-v4.9.1.tar.gz \
    3970542143b7c11e6a09a4d2b50f30a133473c41f15ed0bdcc3b7a1c450d9a5c slirp.tar.gz
fetch https://ftp.gnu.org/gnu/xorriso/xorriso-1.5.6.pl02.tar.gz \
    786f9f5df9865cc5b0c1fecee3d2c0f5e04cab8c9a859bd1c9c7ccd4964fdae1 xorriso.tar.gz

# Use QEMU's vendored, pinned Meson wheel for both projects.
python3.12 -m venv venv
venv/bin/pip install --no-index qemu-10.2.4/python/wheels/meson-*.whl
export PATH="$build_dir/venv/bin:$PATH"
meson setup slirp-build libslirp-v4.9.1 --prefix=/usr/local \
    --libdir=lib64 --default-library=static
meson compile -C slirp-build
meson install -C slirp-build
export PKG_CONFIG_PATH=/usr/local/lib64/pkgconfig

cd qemu-10.2.4
kvm_flag=--disable-kvm
if [[ $(uname -m) == x86_64 ]]; then
    kvm_flag=--enable-kvm
fi
./configure --prefix=/usr/local --python=python3.12 \
    --target-list=x86_64-softmmu --without-default-features \
    --enable-system --enable-tools --enable-tcg "$kvm_flag" \
    --enable-virtfs --enable-attr --enable-slirp \
    --disable-docs --disable-guest-agent --disable-download
make -j"${VM_BUILD_JOBS:-2}"
make DESTDIR="$install_root" install
cd ../xorriso-1.5.6
./configure --prefix=/usr/local --disable-shared --disable-libreadline --disable-libacl --disable-xattr
make -j"${VM_BUILD_JOBS:-2}"
make DESTDIR="$install_root" install

license_dir="$install_root/usr/local/share/licenses/flightctl-vm-runtime"
mkdir -p "$license_dir/qemu" "$license_dir/libslirp" "$license_dir/xorriso"
cp "$build_dir/qemu-10.2.4/"{COPYING,COPYING.LIB,LICENSE} "$license_dir/qemu/"
cp "$build_dir/libslirp-v4.9.1/COPYRIGHT" "$license_dir/libslirp/"
cp "$build_dir/xorriso-1.5.6/"{COPYING,COPYRIGHT} "$license_dir/xorriso/"

# The launcher uses the mkisofs-compatible interface, supplied by xorriso.
cat > "$install_root/usr/local/bin/genisoimage" <<'WRAPPER'
#!/bin/sh
exec /usr/local/bin/xorriso -as mkisofs "$@"
WRAPPER
chmod 0755 "$install_root/usr/local/bin/genisoimage"

# Catch an accidentally disabled device/backend during the image build.
"$install_root/usr/local/bin/qemu-system-x86_64" -device help | grep -q virtio-9p-pci
"$install_root/usr/local/bin/qemu-system-x86_64" -netdev help | grep -q user

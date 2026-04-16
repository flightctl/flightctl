#!/bin/bash
# TPM passthrough setup functions for the test VM.
# Sourced by create_vm_libvirt.sh when ENABLE_TPM_PASSTHROUGH=true.
#
# Expected variables from the caller:
#   SSH_PRIVATE_KEY_PATH, USER, VM_DEFAULT_IP, QEMU_TPM_PREFIX,
#   QEMU_BUILT_FROM_SOURCE, ENABLE_TPM_PASSTHROUGH

# Replaces the VM's qemu-kvm binary with the custom TPM passthrough build.
# Both the binary and firmware/share files are copied — the custom QEMU resolves
# its datadir as /usr/libexec/../share/qemu/ and needs BIOS ROMs, keymaps, and
# VGA firmware that the system qemu-kvm package doesn't provide.
# If the host built QEMU from source, that binary may be linked against
# host-specific libraries so the pre-built CS9-compatible artifact is pulled
# separately for the VM.
replace_vm_qemu_with_custom_build() {
    local qemu_vm_dir="${QEMU_TPM_PREFIX}"
    if [ "${QEMU_BUILT_FROM_SOURCE}" = "true" ]; then
        echo "Host QEMU was built from source, pulling CS9-compatible artifact for VM..."
        qemu_vm_dir="/tmp/qemu-tpm-vm"
        rm -rf "${qemu_vm_dir}"
        if ! pull_qemu_artifact "${qemu_vm_dir}"; then
            echo "WARNING: Failed to pull pre-built artifact, falling back to host-built files"
            qemu_vm_dir="${QEMU_TPM_PREFIX}"
        fi
    fi

    local qemu_vm_binary="${qemu_vm_dir}/bin/qemu-system-x86_64"
    local qemu_vm_share="${qemu_vm_dir}/share/qemu"

    if [ ! -f "${qemu_vm_binary}" ] || [ ! -d "${qemu_vm_share}" ]; then
        echo "WARNING: TPM passthrough enabled but custom QEMU files not found"
        echo "  Binary: ${qemu_vm_binary} (exists: $([ -f "${qemu_vm_binary}" ] && echo yes || echo no))"
        echo "  Share:  ${qemu_vm_share} (exists: $([ -d "${qemu_vm_share}" ] && echo yes || echo no))"
        echo "Agent VMs inside this VM will not be able to use TPM passthrough"
        return 1
    fi

    echo "Replacing VM's qemu-kvm with custom build (TPM passthrough)..."

    local qemu_tarball="/tmp/qemu-tpm-vm-files.tar.gz"
    tar czf "${qemu_tarball}" -C "${qemu_vm_dir}" bin/qemu-system-x86_64 share/qemu

    scp -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        "${qemu_tarball}" ${USER}@${VM_DEFAULT_IP}:/tmp/qemu-tpm-vm-files.tar.gz

    ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        ${USER}@${VM_DEFAULT_IP} bash -s <<'QEMU_REPLACE_EOF'
set -e
mkdir -p /tmp/qemu-tpm-extract
tar xzf /tmp/qemu-tpm-vm-files.tar.gz -C /tmp/qemu-tpm-extract

sudo cp /usr/libexec/qemu-kvm /usr/libexec/qemu-kvm.orig
sudo mv /tmp/qemu-tpm-extract/bin/qemu-system-x86_64 /usr/libexec/qemu-kvm
sudo chown root:root /usr/libexec/qemu-kvm
sudo chmod 755 /usr/libexec/qemu-kvm
sudo restorecon /usr/libexec/qemu-kvm

# The custom QEMU resolves its datadir as /usr/libexec/../share/qemu/ = /usr/share/qemu/.
# Remove conflicting symlinks before copying the real directories.
sudo mkdir -p /usr/share/qemu
for item in /usr/share/qemu/*; do
    if [ -L "$item" ] && [ -d "$item" ]; then
        sudo rm -f "$item"
    fi
done
sudo cp -r /tmp/qemu-tpm-extract/share/qemu/* /usr/share/qemu/
sudo ln -sf /usr/share/qemu-kvm/* /usr/share/qemu/ 2>/dev/null || true
sudo ln -sf /usr/share/ipxe/qemu/* /usr/share/qemu/ 2>/dev/null || true

rm -rf /tmp/qemu-tpm-extract /tmp/qemu-tpm-vm-files.tar.gz
echo "qemu-kvm replaced with custom TPM passthrough build"
/usr/libexec/qemu-kvm -tpmdev help 2>&1
QEMU_REPLACE_EOF

    rm -f "${qemu_tarball}"
    if [ "${qemu_vm_dir}" = "/tmp/qemu-tpm-vm" ]; then
        rm -rf "${qemu_vm_dir}"
    fi
}

# Configures TPM device access inside the VM for the e2e test user.
# Sets udev rules for /dev/tpm0 group access and adds the user to the tss group
# so libvirt session mode can open the device.
configure_vm_tpm_access() {
    echo "Configuring TPM device access inside the VM..."
    ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        ${USER}@${VM_DEFAULT_IP} bash -s <<'TPM_SETUP_EOF'
set -e
echo 'KERNEL=="tpm[0-9]*", MODE="0660", OWNER="tss", GROUP="tss"' | sudo tee /etc/udev/rules.d/99-tpm-group.rules
sudo udevadm control --reload-rules
sudo udevadm trigger /dev/tpm0 2>/dev/null || true
sudo usermod -aG tss $(whoami)
TPM_SETUP_EOF
    echo "TPM device access configured"
}

# Runs diagnostics inside the VM to verify the TPM and QEMU setup.
run_vm_tpm_diagnostics() {
    echo ""
    echo "========================================"
    echo "  VM TPM/QEMU Diagnostics"
    echo "========================================"
    ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        ${USER}@${VM_DEFAULT_IP} bash -s "${ENABLE_TPM_PASSTHROUGH}" <<'DIAG_EOF'
ENABLE_TPM_PASSTHROUGH="$1"

echo "=== QEMU binary ==="
/usr/libexec/qemu-kvm --version 2>&1
/usr/libexec/qemu-kvm -tpmdev help 2>&1

echo ""
echo "=== TPM devices ==="
ls -la /dev/tpm* 2>/dev/null || echo "No TPM devices found"

if [ "${ENABLE_TPM_PASSTHROUGH}" = "true" ]; then
    echo ""
    echo "=== TPM vendor (passthrough verification) ==="
    sudo tpm2_getcap properties-fixed 2>&1 | grep -A1 'TPM2_PT_MANUFACTURER\|TPM2_PT_VENDOR_STRING_1'
    VENDOR=$(sudo tpm2_getcap properties-fixed 2>&1 | grep -A1 TPM2_PT_VENDOR_STRING_1 | grep value | awk -F'"' '{print $2}')
    if echo "${VENDOR}" | grep -qi "^SW"; then
        echo "WARNING: TPM reports as software TPM (vendor: ${VENDOR})"
    else
        echo "OK: TPM reports as hardware TPM (vendor: ${VENDOR})"
    fi

    echo ""
    echo "=== TPM device permissions ==="
    ls -la /dev/tpm0 /dev/tpmrm0 2>/dev/null
    echo "Current user: $(whoami)"
    echo "Groups: $(groups)"
    echo "tss group access: $(id -nG | grep -q tss && echo YES || echo NO)"

    echo ""
    echo "=== TPM persistent handles ==="
    sudo tpm2_getcap handles-persistent 2>&1

    echo ""
    echo "=== TPM resource limits ==="
    sudo tpm2_getcap properties-fixed 2>&1 | grep -A1 'TPM2_PT_HR_TRANSIENT_MIN\|TPM2_PT_HR_PERSISTENT_MIN\|TPM2_PT_HR_LOADED_MIN'
fi

echo ""
echo "=== System mode domcapabilities (TPM section) ==="
sudo virsh domcapabilities 2>/dev/null | grep -A 10 '<tpm' || echo "Failed to get system domcapabilities"

echo ""
echo "=== Session mode domcapabilities (TPM section) ==="
virsh -c qemu:///session domcapabilities 2>/dev/null | grep -A 10 '<tpm' || echo "Failed to get session domcapabilities"

echo ""
echo "=== Session mode emulator path ==="
virsh -c qemu:///session domcapabilities 2>/dev/null | grep '<path>' || echo "Failed to get session emulator path"

echo ""
echo "=== libvirtd status ==="
sudo systemctl is-active libvirtd 2>&1
DIAG_EOF
}

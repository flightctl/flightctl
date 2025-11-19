#!/usr/bin/env bash
set -euo pipefail

# Boot a VM from a qcow2 overlay backed by the base image and open its console.
# Defaults can be overridden via environment variables:
#   BASE=/path/to/base.qcow2 VM_NAME=name OVERLAY=/path/to/overlay.qcow2
#
# Exit console with: Ctrl+]

BASE="${BASE:-/home/iskornya/code/flightctl/bin/output/qcow2/disk.qcow2}"
VM_NAME="${VM_NAME:-flightctl-console}"
OVERLAY="${OVERLAY:-/var/lib/libvirt/images/${VM_NAME}.qcow2}"
RAM_MB="${RAM_MB:-2048}"
VCPUS="${VCPUS:-2}"
SSH_FWD_PORT="${SSH_FWD_PORT:-2222}"
LIBVIRT_DIR="/var/lib/libvirt/images"
BASE_COPY="${BASE_COPY:-${LIBVIRT_DIR}/${VM_NAME}_base.qcow2}"

if [[ ! -f "$BASE" ]]; then
  echo "Base image not found: $BASE" >&2
  exit 1
fi

echo "Base:    $BASE"
echo "VM Name: $VM_NAME"
echo "Overlay: $OVERLAY"
echo "BaseCopy:$BASE_COPY"
echo "SSH Port:${SSH_FWD_PORT}"

# Ensure libvirtd is running
sudo systemctl start libvirtd || true

# Clean any previous VM with the same name
sudo virsh destroy "$VM_NAME" >/dev/null 2>&1 || true
sudo virsh undefine "$VM_NAME" --nvram >/dev/null 2>&1 || true
sudo rm -f "$OVERLAY"

# Place base under libvirt storage (SELinux-friendly) and label
echo "Copying base image into $BASE_COPY ..."
sudo cp -f --sparse=always "$BASE" "$BASE_COPY"
sudo chmod 0644 "$BASE_COPY"
sudo restorecon -Rv "$LIBVIRT_DIR" >/dev/null 2>&1 || true

# Create overlay backed by the copied base
echo "Creating overlay backed by $BASE_COPY ..."
sudo qemu-img create -f qcow2 -b "$BASE_COPY" -F qcow2 "$OVERLAY" >/dev/null
sudo chmod 0644 "$OVERLAY"
sudo restorecon -Rv "$OVERLAY" >/dev/null 2>&1 || true
sudo qemu-img info "$OVERLAY"

# Ensure default network is available
sudo virsh net-start default >/dev/null 2>&1 || true
sudo virsh net-autostart default >/dev/null 2>&1 || true

# Boot the VM headless with UEFI, q35, serial console
echo "Booting VM..."
sudo virt-install \
  --name "$VM_NAME" \
  --memory "$RAM_MB" \
  --vcpus "$VCPUS" \
  --network none \
  --disk "path=$OVERLAY,format=qcow2" \
  --import \
  --os-variant centos-stream9 \
  --machine q35 \
  --graphics none \
  --noautoconsole \
  --cpu "Haswell-noTSX-IBRS" \
  --boot uefi \
  --qemu-commandline="-netdev user,id=n0,hostfwd=tcp::${SSH_FWD_PORT}-:22 -device virtio-net-pci,netdev=n0,bus=pcie.0,addr=0x10"

echo "Connecting to console (Ctrl+] to exit)..."
sudo virsh console "$VM_NAME"



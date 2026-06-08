#!/bin/bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions
KUBECONFIG_PATH="${1:-/home/kni/clusterconfigs/auth/kubeconfig}"
echo "kubeconfig path is: ${KUBECONFIG_PATH}"
export KUBECONFIG=${KUBECONFIG_PATH}

# Variables
VM_NAME="test-vm"
VM_RAM=${VM_RAM:-24576}                 # RAM in MB necessary to run the flightctl e2e
VM_CPUS=${VM_CPUS:-12}                  # Number of CPUs
VM_DISK_SIZE_INC=${VM_DISK_SIZE_INC:-30} # Disk size increment
NETWORK_NAME="$(get_ocp_nodes_network)"   # Network name
NETWORK_NAME=${NETWORK_NAME:-flightctl-net}
DEFAULT_NETWORK_NAME="default"
echo "ocp_network name is: ${NETWORK_NAME}"
echo "Disk size increment: ${VM_DISK_SIZE_INC}G"
ISO_URL="https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-x86_64-9-latest.x86_64.qcow2"
DISK_PATH="/var/lib/libvirt/images/${VM_NAME}.qcow2"
DISK_PATH_SRC="/var/lib/libvirt/images/${VM_NAME}_src.qcow2"
CIDATA_ISO="/var/lib/libvirt/images/${VM_NAME}-cidata.iso"
TIMEOUT_SECONDS=60
CHECK_INTERVAL=10
USER="kni"
USER_HOME="/home/${USER}"   # user $HOME in the vm
SSH_PRIVATE_KEY_PATH="/home/${USER}/.ssh/id_rsa"
SSH_PUBLIC_KEY_PATH="/home/${USER}/.ssh/id_rsa.pub"
REMOTE_URL=$(git remote get-url origin 2>/dev/null || echo "https://github.com/flightctl/flightctl.git")
DISABLE_TPM_PASSTHROUGH=${DISABLE_TPM_PASSTHROUGH:-}
QEMU_TPM_PREFIX="/usr/local/qemu-tpm"
QEMU_TPM_IMAGE="${QEMU_TPM_IMAGE:-quay.io/kkyrazis/qemu-tpm:v10.1.0}"

# Pulls the pre-built QEMU artifact from the registry into a target directory.
pull_qemu_artifact() {
    local target_dir="$1"
    echo "Pulling pre-built QEMU from ${QEMU_TPM_IMAGE}..."
    if podman artifact pull "${QEMU_TPM_IMAGE}" 2>/dev/null; then
        sudo mkdir -p "${target_dir}"
        podman artifact extract "${QEMU_TPM_IMAGE}" /tmp/
        sudo tar xzf /tmp/qemu-tpm-*.tar.gz --strip-components=3 -C "${target_dir}"
        rm -f /tmp/qemu-tpm-*.tar.gz
        podman artifact rm "${QEMU_TPM_IMAGE}" 2>/dev/null || true
        return 0
    fi
    echo "Failed to pull pre-built QEMU artifact"
    return 1
}

# Ensures a working custom QEMU build with TPM passthrough is available at QEMU_TPM_PREFIX.
# Tries in order: existing local build, pull pre-built artifact from registry, build from source.
# Sets QEMU_BUILT_FROM_SOURCE=true if the host required a local build, meaning the binary
# may not be compatible with the CentOS Stream 9 VM.
QEMU_BUILT_FROM_SOURCE=false
ensure_custom_qemu() {
    local qemu_bin="${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64"

    if [ -f "${qemu_bin}" ] && "${qemu_bin}" --version >/dev/null 2>&1; then
        echo "Custom QEMU already installed and working at ${QEMU_TPM_PREFIX}"
        return 0
    fi

    if pull_qemu_artifact "${QEMU_TPM_PREFIX}"; then
        if [ -f "${qemu_bin}" ] && "${qemu_bin}" --version >/dev/null 2>&1; then
            echo "Pre-built QEMU installed successfully at ${QEMU_TPM_PREFIX}"
            return 0
        fi
        echo "Pre-built QEMU binary not compatible with this host"
        sudo rm -rf "${QEMU_TPM_PREFIX}"
    fi

    echo "Building QEMU from source (this may take a while)..."
    "${SCRIPT_DIR}/build_qemu_tpm_from_source.sh"
    if [ -f "${qemu_bin}" ] && "${qemu_bin}" --version >/dev/null 2>&1; then
        echo "QEMU built from source successfully at ${QEMU_TPM_PREFIX}"
        QEMU_BUILT_FROM_SOURCE=true
        return 0
    fi

    echo "ERROR: Failed to obtain a working custom QEMU build"
    return 1
}

# TPM passthrough detection
# ENABLE_TPM_PASSTHROUGH gates all TPM-related logic in this script (custom QEMU setup,
# VM XML manipulation, and copying QEMU files into the VM). It is only set when:
#   1. DISABLE_TPM_PASSTHROUGH is not truthy
#   2. /dev/tpm0 exists on the host
#   3. The device is TPM 2.0
# Passthrough gives the VM exclusive access to /dev/tpm0; only one VM can hold
# it at a time. The device is released when the VM is destroyed or undefined.
ENABLE_TPM_PASSTHROUGH=false
USE_CUSTOM_QEMU=false
if [[ "${DISABLE_TPM_PASSTHROUGH}" =~ ^(true|1|yes)$ ]]; then
    echo "TPM passthrough disabled via DISABLE_TPM_PASSTHROUGH"
elif [ ! -e /dev/tpm0 ]; then
    echo "No /dev/tpm0 found, skipping TPM passthrough"
else
    TPM_VERSION=$(cat /sys/class/tpm/tpm0/tpm_version_major 2>/dev/null || echo "")
    if [ "${TPM_VERSION}" != "2" ]; then
        echo "TPM device is not TPM 2.0 (version: ${TPM_VERSION:-unknown}), skipping passthrough"
    elif virsh domcapabilities 2>/dev/null | grep -q '<value>passthrough</value>'; then
        echo "TPM 2.0 device detected at /dev/tpm0, enabling passthrough (native support)"
        ENABLE_TPM_PASSTHROUGH=true
    else
        echo "Libvirt does not natively support TPM passthrough, checking for custom QEMU..."
        if ensure_custom_qemu; then
            echo "TPM 2.0 device detected, using custom QEMU build for passthrough"
            ENABLE_TPM_PASSTHROUGH=true
            USE_CUSTOM_QEMU=true
        else
            echo "WARNING: TPM 2.0 device found but no working QEMU with passthrough available"
            virsh domcapabilities 2>/dev/null | grep -A 10 '<tpm'
        fi
    fi
fi

# Remove existing VM
echo "Removing existing VM ${VM_NAME}..."
virsh destroy ${VM_NAME}
virsh undefine ${VM_NAME}

# Get the image
if [ ! -f $DISK_PATH_SRC ]; then
    echo "Source image ${DISK_PATH_SRC} not found! Downloading it..."
    curl -o ${DISK_PATH_SRC} ${ISO_URL}
fi

if [ -f $DISK_PATH ]; then
    echo "Removing existing ${DISK_PATH}..."
    rm ${DISK_PATH}
fi

echo "Copying ${DISK_PATH_SRC} to ${DISK_PATH}..."
cp ${DISK_PATH_SRC} ${DISK_PATH}

# Resize the VM image to make room for the increased number of agent-images to be saved
echo "Resizing image ${DISK_PATH}..."
qemu-img resize ${DISK_PATH} +${VM_DISK_SIZE_INC}G && \
qemu-img info --output=json "${DISK_PATH}"

# Create a NoCloud ISO for cloud-init instead of relying on virt-install's
# --cloud-init flag. This allows us to use virt-install --print-xml to generate
# the domain XML, modify it (TPM, emulator), and define+start in a single boot.
echo "Creating cloud-init NoCloud ISO..."
SSH_PUBLIC_KEY="$(cat ${SSH_PUBLIC_KEY_PATH})"
CIDATA_TMPDIR=$(mktemp -d)

cat > ${CIDATA_TMPDIR}/user-data << _EOF_
#cloud-config
users:
  - name: ${USER}
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ${SSH_PUBLIC_KEY}
_EOF_

cat > ${CIDATA_TMPDIR}/meta-data << _EOF_
instance-id: ${VM_NAME}
local-hostname: ${VM_NAME}
_EOF_

genisoimage -output "${CIDATA_ISO}" -volid cidata -joliet -rock \
    "${CIDATA_TMPDIR}/user-data" "${CIDATA_TMPDIR}/meta-data"
rm -rf "${CIDATA_TMPDIR}"

# Restart default network
sudo virsh net-destroy ${DEFAULT_NETWORK_NAME}
sudo virsh net-start ${DEFAULT_NETWORK_NAME}

# Generate the VM domain XML with virt-install --print-xml, then modify it with
# python3 to inject TPM passthrough, the cloud-init ISO, and optionally swap the
# emulator binary. This avoids passing --tpm to virt-install (which fails on hosts
# where domcapabilities doesn't advertise passthrough) and eliminates the need to
# create the VM, stop it, edit the XML, and restart it.
echo "Generating VM domain XML..."
VM_XML="/tmp/${VM_NAME}.xml"

virt-install --print-xml \
    --name $VM_NAME \
    --memory $VM_RAM \
    --vcpus $VM_CPUS \
    --disk path=$DISK_PATH,format=qcow2 \
    --os-variant centos-stream9 \
    --network network=$DEFAULT_NETWORK_NAME \
    --network network=$NETWORK_NAME,model=virtio \
    --import \
    --cpu host-model \
    --graphics none \
    --noautoconsole \
    > "${VM_XML}"

CUSTOM_EMULATOR=""
Q35_MACHINE=""
if [ "${USE_CUSTOM_QEMU}" = "true" ]; then
    CUSTOM_EMULATOR="${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64"
    sudo chcon -t qemu_exec_t "${CUSTOM_EMULATOR}"
    Q35_MACHINE=$("${CUSTOM_EMULATOR}" -machine help 2>/dev/null | grep "alias of" | grep q35 | awk '{print $1}')
    Q35_MACHINE=${Q35_MACHINE:-q35}
    echo "Using custom QEMU emulator: ${CUSTOM_EMULATOR} (machine: ${Q35_MACHINE})"
fi

TPM_PASSTHROUGH="${ENABLE_TPM_PASSTHROUGH}"

# This always runs to inject the cloud-init ISO (and optionally the custom emulator).
# TPM passthrough XML is only added when ENABLE_TPM_PASSTHROUGH=true.
python3 - "${VM_XML}" "${CIDATA_ISO}" "${TPM_PASSTHROUGH}" "${CUSTOM_EMULATOR}" "${Q35_MACHINE}" << 'PYEOF'
import sys
import xml.etree.ElementTree as ET

xml_file = sys.argv[1]
cidata_iso = sys.argv[2]
tpm_passthrough = sys.argv[3] == "true"
custom_emulator = sys.argv[4] if len(sys.argv) > 4 else ""
q35_machine = sys.argv[5] if len(sys.argv) > 5 else ""

tree = ET.parse(xml_file)
root = tree.getroot()
devices = root.find("devices")

if tpm_passthrough:
    tpm = ET.SubElement(devices, "tpm")
    tpm.set("model", "tpm-tis")
    backend = ET.SubElement(tpm, "backend")
    backend.set("type", "passthrough")
    device = ET.SubElement(backend, "device")
    device.set("path", "/dev/tpm0")

disk = ET.SubElement(devices, "disk")
disk.set("type", "file")
disk.set("device", "cdrom")
driver = ET.SubElement(disk, "driver")
driver.set("name", "qemu")
driver.set("type", "raw")
source = ET.SubElement(disk, "source")
source.set("file", cidata_iso)
target = ET.SubElement(disk, "target")
target.set("dev", "sdb")
target.set("bus", "sata")
ET.SubElement(disk, "readonly")

if custom_emulator:
    emulator = devices.find("emulator")
    if emulator is not None:
        emulator.text = custom_emulator
    if q35_machine:
        os_type = root.find("os/type")
        if os_type is not None:
            os_type.set("machine", q35_machine)

tree.write(xml_file, xml_declaration=True, encoding="unicode")
PYEOF

echo "Creating virtual machine ${VM_NAME}..."
virsh define "${VM_XML}"
virsh start "${VM_NAME}"
rm -f "${VM_XML}"

wait_for_vm_ips() {
  echo "Waiting for VM IPs to be available (timeout: ${TIMEOUT_SECONDS}s, checking every ${CHECK_INTERVAL}s)..."
  ELAPSED=0
  VM_DEFAULT_IP=""
  VM_IP=""

  while [ $ELAPSED -lt $TIMEOUT_SECONDS ]; do
    # Get the VM interfaces
    export INTERFACE_DEFAULT=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep default | awk '{print $1}')
    export INTERFACE_BM=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep ${NETWORK_NAME} | awk '{print $1}')

    if [ -n "${INTERFACE_DEFAULT}" ]; then
      VM_DEFAULT_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_DEFAULT} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
    fi

    if [ -n "${INTERFACE_BM}" ]; then
      if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
        VM_IP=$(sudo virsh domifaddr "${VM_NAME}" --interface "${INTERFACE_BM}" 2>/dev/null | awk '/ipv6/ {print $4}' | cut -d'/' -f1 | grep -v '^fe80' | head -1)
      else
        VM_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_BM} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
      fi
    fi

    # Check if both VM's IPs are available
    if [ -n "${VM_DEFAULT_IP}" ] && [ -n "${VM_IP}" ]; then
      echo "VM IPs are available!"
      echo "VM DEFAULT IP: ${VM_DEFAULT_IP}"
      echo "VM IP: ${VM_IP}"
      return 0
    fi

    echo "Waiting for VM IPs... (${ELAPSED}s/${TIMEOUT_SECONDS}s) - DEFAULT_IP: ${VM_DEFAULT_IP:-not available}, BM_IP: ${VM_IP:-not available}"
    sleep ${CHECK_INTERVAL}
    ELAPSED=$((ELAPSED + CHECK_INTERVAL))
  done

  echo "ERROR: Failed to get VM IPs within ${TIMEOUT_SECONDS} seconds"
  echo "VM DEFAULT IP: ${VM_DEFAULT_IP:-not available}"
  echo "VM IP: ${VM_IP:-not available}"
  return 1
}

wait_for_ssh() {
  local ssh_timeout="${1:-${TIMEOUT_SECONDS}}"
  echo "Waiting for SSH to be ready on ${SSH_VM_TARGET} (timeout: ${ssh_timeout}s)..."
  ELAPSED=0
  while [ $ELAPSED -lt $ssh_timeout ]; do
    if ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o BatchMode=yes -o ConnectTimeout=5 ${SSH_VM_TARGET} "true" 2>/dev/null; then
      echo "SSH is ready"
      return 0
    fi
    sleep ${CHECK_INTERVAL}
    ELAPSED=$((ELAPSED + CHECK_INTERVAL))
  done
  echo "ERROR: SSH did not become ready within ${ssh_timeout} seconds"
  echo "VM state: $(virsh domstate ${VM_NAME} 2>/dev/null || echo 'unknown')"
  virsh domifaddr ${VM_NAME} 2>/dev/null || true
  ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes -o ConnectTimeout=5 -v ${SSH_VM_TARGET} "true" 2>&1 || true
  return 1
}

wait_for_vm_ips || exit 1

# Configure the VM
echo "Provisioning the VM..."

if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
  SSH_VM_TARGET="${USER}@${VM_IP}"
else
  SSH_VM_TARGET="${USER}@${VM_DEFAULT_IP}"
fi

wait_for_ssh || exit 1

# Executing commands
echo "Executing commands in the VM..."

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${SSH_VM_TARGET} bash -s <<'REMOTE_EOF'
set -e
  # Install packages; retry on failure (e.g. baseos mirror/checksum).
  install_pkgs() {
    sudo dnf install -y epel-release libvirt libvirt-client virt-install pam-devel swtpm tpm2-tools wget \
                      make golang git \
                      podman qemu-kvm sshpass skopeo
    sudo dnf --enablerepo=crb install -y libvirt-devel
    sudo rpm --import https://dl.google.com/linux/linux_signing_key.pub && \
    echo -e "[google-chrome]\nname=google-chrome\nbaseurl=http://dl.google.com/linux/chrome/rpm/stable/x86_64\nenabled=1\ngpgcheck=1\ngpgkey=https://dl.google.com/linux/linux_signing_key.pub" | sudo tee /etc/yum.repos.d/google-chrome.repo && \
    sudo dnf -y install google-chrome-stable
  }
  for attempt in 1 2 3 4; do
    install_pkgs && break
    if [ "$attempt" -lt 4 ]; then
      echo "dnf install failed (attempt $attempt/4), clearing cache and retrying in 10s..."
      sudo dnf clean all
      sudo rm -rf /var/cache/dnf
      sudo dnf makecache
      sleep 10
    else
      echo "dnf install failed after 4 attempts"
      exit 1
    fi
  done
REMOTE_EOF

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${SSH_VM_TARGET} <<EOF
  # Install OpenShift client
  echo "Installing OpenShift client..."
  curl https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/stable/openshift-client-linux-amd64-rhel9.tar.gz | sudo tar xvz -C /usr/local/bin

  echo "Cloning $REMOTE_URL to $USER_HOME/..."
  git clone $REMOTE_URL $USER_HOME/flightctl

  # Install Helm
  echo "Installing Helm..."
  $USER_HOME/flightctl/test/scripts/install_helm.sh

  # Verify helm installation and ensure it's in PATH
  if ! command -v helm &>/dev/null; then
    echo "ERROR: helm installation failed or not in PATH"
    echo "Checking common locations..."
    ls -la /usr/local/bin/helm || echo "Not in /usr/local/bin"
    ls -la /usr/bin/helm || echo "Not in /usr/bin"
    exit 1
  fi
  echo "Helm installed successfully: \$(helm version --short)"

  # Enable libvirtd service
  sudo systemctl enable --now libvirtd

  # Configure Kind for rootless operation
  echo "Configuring Kind for rootless operation in Linux..."
  sudo mkdir -p /etc/systemd/system/user@.service.d
  cat <<EOF2 | sudo tee /etc/systemd/system/user@.service.d/delegate.conf > /dev/null
[Service]
Delegate=yes
EOF2
EOF

# Set up TPM passthrough inside the VM: replace qemu-kvm, configure device
# access, and run diagnostics to verify the setup.
if [ "${ENABLE_TPM_PASSTHROUGH}" = "true" ]; then
    source "${SCRIPT_DIR}/tpm_passthrough.sh"
    replace_vm_qemu_with_custom_build || { echo "ERROR: Failed to replace QEMU with custom TPM build"; exit 1; }
    configure_vm_tpm_access || { echo "ERROR: Failed to configure TPM device access"; exit 1; }
    run_vm_tpm_diagnostics
fi

# Greetings
echo "You can access the created VM with ssh ${SSH_VM_TARGET}"
echo "VM creation and provisioning complete!"

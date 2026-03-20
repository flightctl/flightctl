#!/bin/bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions
KUBECONFIG_PATH="${1:-/home/kni/clusterconfigs/auth/kubeconfig}"
echo "kubeconfig path is: ${KUBECONFIG_PATH}"
export KUBECONFIG=${KUBECONFIG_PATH}

# Variables
VM_NAME="test-vm"
VM_RAM=${VM_RAM:-10240}                # RAM in MB necessary to run the flightctl e2e
VM_CPUS=${VM_CPUS:-8}                  # Number of CPUs
VM_DISK_SIZE_INC=${VM_DISK_SIZE_INC:-30} # Disk size increment
NETWORK_NAME="$(get_ocp_nodes_network)"   # Network name
NETWORK_NAME=${NETWORK_NAME:-flightctl-net}
DEFAULT_NETWORK_NAME="default"
echo "ocp_network name is: ${NETWORK_NAME}"
echo "Disk size increment: ${VM_DISK_SIZE_INC}G"
ISO_URL="https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-x86_64-9-latest.x86_64.qcow2"
DISK_PATH="/var/lib/libvirt/images/${VM_NAME}.qcow2"
DISK_PATH_SRC="/var/lib/libvirt/images/${VM_NAME}_src.qcow2"
VIRT_BRIDGE="virbr0"         # Default libvirt bridge
TIMEOUT_SECONDS=60
CHECK_INTERVAL=10
USER="kni"
USER_HOME="/home/${USER}"   # user $HOME in the vm
SSH_PRIVATE_KEY_PATH="/home/${USER}/.ssh/id_rsa"
SSH_PUBLIC_KEY_PATH="/home/${USER}/.ssh/id_rsa.pub"
USER_DATA_FILE="user-data.yaml"
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
        sudo tar xzf /tmp/qemu-tpm-*.tar.gz -C "${target_dir}"
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
# TPM_PASSTHROUGH_ARGS gates all TPM-related logic in this script (custom QEMU setup,
# VM XML manipulation, and copying the QEMU binary into the VM). It is only set when:
#   1. DISABLE_TPM_PASSTHROUGH is not truthy
#   2. /dev/tpm0 exists on the host
#   3. The device is TPM 2.0
# Passthrough gives the VM exclusive access to /dev/tpm0; only one VM can hold
# it at a time. The device is released when the VM is destroyed or undefined.
TPM_PASSTHROUGH_ARGS=""
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
        TPM_PASSTHROUGH_ARGS="--tpm /dev/tpm0,model=tpm-tis,backend.type=passthrough"
    else
        echo "Libvirt does not natively support TPM passthrough, checking for custom QEMU..."
        if ensure_custom_qemu; then
            echo "TPM 2.0 device detected, using custom QEMU build for passthrough"
            TPM_PASSTHROUGH_ARGS="--tpm /dev/tpm0,model=tpm-tis,backend.type=passthrough"
        else
            echo "WARNING: TPM 2.0 device found but no working QEMU with passthrough available"
            virsh domcapabilities 2>/dev/null | grep -A 10 '<tpm'
        fi
    fi
fi

# Generate user-data file
echo "Generating user-data file..."
SSH_PUBLIC_KEY="$(cat ${SSH_PUBLIC_KEY_PATH})"

cat > ${USER_DATA_FILE} << _EOF_
#cloud-config
users:
  - name: ${USER}
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ${SSH_PUBLIC_KEY}
_EOF_

cat ${USER_DATA_FILE}

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

# Restart default network
sudo virsh net-destroy ${DEFAULT_NETWORK_NAME}
sudo virsh net-start ${DEFAULT_NETWORK_NAME}

# Create the VM
echo "Creating virtual machine ${VM_NAME}..."
USE_CUSTOM_QEMU=false
if [ -n "${TPM_PASSTHROUGH_ARGS}" ] && [ -d "${QEMU_TPM_PREFIX}" ]; then
    USE_CUSTOM_QEMU=true
fi

# When libvirt natively supports passthrough, pass TPM args directly to virt-install.
# When using a custom QEMU build, we create the VM without TPM first, then modify the
# XML post-creation to swap the emulator and inject TPM passthrough configuration.
VIRT_INSTALL_TPM_ARGS=""
if [ -n "${TPM_PASSTHROUGH_ARGS}" ] && [ "${USE_CUSTOM_QEMU}" = "false" ]; then
    VIRT_INSTALL_TPM_ARGS="${TPM_PASSTHROUGH_ARGS}"
fi

virt-install \
  --name $VM_NAME \
  --memory $VM_RAM \
  --vcpus $VM_CPUS \
  --disk path=$DISK_PATH,format=qcow2 \
  --os-variant centos-stream9  \
  --network network=$DEFAULT_NETWORK_NAME \
  --network network=$NETWORK_NAME,model=virtio \
  --import \
  --cpu host-model \
  --graphics none \
  --noautoconsole \
  --cloud-init disable=on,user-data=user-data.yaml \
  ${VIRT_INSTALL_TPM_ARGS}

wait_for_vm_ips() {
  echo "Waiting for VM IPs to be available (timeout: ${TIMEOUT_SECONDS}s, checking every ${CHECK_INTERVAL}s)..."
  ELAPSED=0
  VM_DEFAULT_IP=""
  VM_IP=""

  while [ $ELAPSED -lt $TIMEOUT_SECONDS ]; do
    export INTERFACE_DEFAULT=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep default | awk '{print $1}')
    export INTERFACE_BM=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep ${NETWORK_NAME} | awk '{print $1}')

    if [ -n "${INTERFACE_DEFAULT}" ]; then
      VM_DEFAULT_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_DEFAULT} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
    fi

    if [ -n "${INTERFACE_BM}" ]; then
      VM_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_BM} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
    fi

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
  echo "Waiting for SSH to be ready on ${VM_DEFAULT_IP} (timeout: ${ssh_timeout}s)..."
  ELAPSED=0
  while [ $ELAPSED -lt $ssh_timeout ]; do
    if ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o BatchMode=yes -o ConnectTimeout=5 ${USER}@${VM_DEFAULT_IP} "true" 2>/dev/null; then
      echo "SSH is ready"
      return 0
    fi
    sleep ${CHECK_INTERVAL}
    ELAPSED=$((ELAPSED + CHECK_INTERVAL))
  done
  echo "ERROR: SSH did not become ready within ${ssh_timeout} seconds"
  echo "VM state: $(virsh domstate ${VM_NAME} 2>/dev/null || echo 'unknown')"
  virsh domifaddr ${VM_NAME} 2>/dev/null || true
  # Attempt SSH with verbose output for diagnostics
  ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes -o ConnectTimeout=5 -v ${USER}@${VM_DEFAULT_IP} "true" 2>&1 || true
  return 1
}

# Wait for IPs and SSH so cloud-init completes before any VM reconfiguration
wait_for_vm_ips || exit 1
wait_for_ssh || exit 1

# If TPM passthrough is enabled and a custom QEMU build is available,
# stop the VM, swap the emulator, add TPM passthrough XML, and restart.
# This must happen before provisioning since the VM gets a new IP after restart.
if [ "${USE_CUSTOM_QEMU}" = "true" ]; then
    echo "Configuring VM for TPM passthrough with custom QEMU..."
    # Disable cloud-init before restarting so it doesn't re-run with empty user-data
    # and overwrite the SSH keys and sudo config injected on first boot.
    ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o BatchMode=yes ${USER}@${VM_DEFAULT_IP} "sudo touch /etc/cloud/cloud-init.disabled"
    sudo chcon -t qemu_exec_t "${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64"
    virsh destroy ${VM_NAME}
    virsh dumpxml --inactive ${VM_NAME} > /tmp/${VM_NAME}.xml
    sed -i "s|<emulator>.*</emulator>|<emulator>${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64</emulator>|" /tmp/${VM_NAME}.xml
    QEMU_Q35_MACHINE=$("${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64" -machine help 2>/dev/null | grep "alias of" | grep q35 | awk '{print $1}')
    QEMU_Q35_MACHINE=${QEMU_Q35_MACHINE:-q35}
    sed -i "s|machine='pc-q35-rhel[^']*'|machine='${QEMU_Q35_MACHINE}'|" /tmp/${VM_NAME}.xml
    sed -i '/<\/devices>/i \    <tpm model="tpm-tis">\n      <backend type="passthrough">\n        <device path="\/dev\/tpm0"\/>\n      <\/backend>\n    <\/tpm>' /tmp/${VM_NAME}.xml
    virsh define /tmp/${VM_NAME}.xml
    virsh start ${VM_NAME}
    rm -f /tmp/${VM_NAME}.xml

    # Wait for the VM to boot with the new emulator. Use a longer SSH timeout
    # since this is a cold boot with a different QEMU binary. The IP wait may
    # return stale DHCP leases from the previous session, so SSH is the real
    # readiness check.
    wait_for_vm_ips || exit 1
    wait_for_ssh 180 || exit 1
fi

# Configure the VM
echo "Provisioning the VM..."

# Executing commands
echo "Executing commands in the VM..."

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${USER}@${VM_DEFAULT_IP} bash -s <<'REMOTE_EOF'
set -e
  # Install packages; retry on failure (e.g. baseos mirror/checksum).
  install_pkgs() {
    sudo dnf install -y epel-release libvirt libvirt-client virt-install pam-devel swtpm wget \
                      make golang git \
                      podman qemu-kvm sshpass skopeo
    sudo dnf --enablerepo=crb install -y libvirt-devel
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

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${USER}@${VM_DEFAULT_IP} <<EOF
  # Install OpenShift client
  echo "Installing OpenShift client..."
  curl https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/stable/openshift-client-linux-amd64-rhel9.tar.gz | sudo tar xvz -C /usr/local/bin

  echo "Cloning $REMOTE_URL to $USER_HOME/..."
  git clone $REMOTE_URL $USER_HOME/flightctl

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

# The VM runs CentOS Stream 9 whose qemu-kvm never supports TPM passthrough.
# Replace it with the custom build so agent VMs inside this VM can use passthrough.
# If the host built QEMU from source, that binary may be linked against host-specific
# libraries. Pull the pre-built CS9-compatible artifact separately for the VM.
if [ -n "${TPM_PASSTHROUGH_ARGS}" ]; then
    QEMU_VM_BINARY="${QEMU_TPM_PREFIX}/bin/qemu-system-x86_64"
    if [ "${QEMU_BUILT_FROM_SOURCE}" = "true" ]; then
        echo "Host QEMU was built from source, pulling CS9-compatible artifact for VM..."
        QEMU_VM_DIR="/tmp/qemu-tpm-vm"
        rm -rf "${QEMU_VM_DIR}"
        if pull_qemu_artifact "${QEMU_VM_DIR}"; then
            QEMU_VM_BINARY="${QEMU_VM_DIR}/bin/qemu-system-x86_64"
        else
            echo "WARNING: Failed to pull pre-built artifact, falling back to host-built binary"
        fi
    fi

    if [ -f "${QEMU_VM_BINARY}" ]; then
        echo "Replacing VM's qemu-kvm with custom build (TPM passthrough)..."
        scp -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            "${QEMU_VM_BINARY}" ${USER}@${VM_DEFAULT_IP}:/tmp/qemu-system-x86_64
        ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            ${USER}@${VM_DEFAULT_IP} "sudo cp /usr/libexec/qemu-kvm /usr/libexec/qemu-kvm.orig && sudo mv /tmp/qemu-system-x86_64 /usr/libexec/qemu-kvm && sudo chmod 755 /usr/libexec/qemu-kvm"
        echo "VM's qemu-kvm replaced with TPM passthrough build (original backed up to /usr/libexec/qemu-kvm.orig)"
        rm -rf "${QEMU_VM_DIR:-}" 2>/dev/null || true
    else
        echo "WARNING: TPM passthrough enabled but no QEMU binary available for VM"
        echo "Agent VMs inside this VM will not be able to use TPM passthrough"
    fi
fi

# Clean up
echo "Cleaning up stuff..."
rm ${USER_DATA_FILE}

# Greetings
echo "You can access the created VM with ssh ${USER}@${VM_IP}"
echo "VM creation and provisioning complete!"

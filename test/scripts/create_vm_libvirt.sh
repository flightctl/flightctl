#!/bin/bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions
KUBECONFIG_PATH="${1:-/home/kni/clusterconfigs/auth/kubeconfig}"
echo "kubeconfig path is: ${KUBECONFIG_PATH}"
export KUBECONFIG=${KUBECONFIG_PATH}

# Variables
VM_NAME="test-vm"
VM_RAM=10240                # RAM in MB necessary to run the flightctl e2e
VM_CPUS=8                  # Number of CPUs
VM_DISK_SIZE_INC=${VM_DISK_SIZE_INC:-30} # Disk size increment
NETWORK_NAME="$(get_ocp_nodes_network)"   # Network name
NETWORK_NAME=${NETWORK_NAME:-baremetal-0}
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
  --cloud-init disable=on,user-data=user-data.yaml

# Wait for the VM to start and get IPs
echo "Waiting for VM IPs to be available (timeout: ${TIMEOUT_SECONDS}s, checking every ${CHECK_INTERVAL}s)..."
ELAPSED=0
VM_DEFAULT_IP=""
VM_IP=""

while [ $ELAPSED -lt $TIMEOUT_SECONDS ]; do
  # Get the VM interfaces
  export INTERFACE_DEFAULT=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep default | awk '{print $1}')
  export INTERFACE_BM=$(sudo virsh domiflist ${VM_NAME} 2>/dev/null | grep ${NETWORK_NAME} | awk '{print $1}')
  
  # Try to get the IPs
  if [ -n "${INTERFACE_DEFAULT}" ]; then
    VM_DEFAULT_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_DEFAULT} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
  fi
  
  if [ -n "${INTERFACE_BM}" ]; then
    VM_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_BM} 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
  fi
  
  # Check if both VM's IPs are available
  if [ -n "${VM_DEFAULT_IP}" ] && [ -n "${VM_IP}" ]; then
    echo "VM IPs are available!"
    echo "VM DEFAULT IP: ${VM_DEFAULT_IP}"
    echo "VM IP: ${VM_IP}"
    break
  fi
  
  echo "Waiting for VM IPs... (${ELAPSED}s/${TIMEOUT_SECONDS}s) - DEFAULT_IP: ${VM_DEFAULT_IP:-not available}, BM_IP: ${VM_IP:-not available}"
  sleep ${CHECK_INTERVAL}
  ELAPSED=$((ELAPSED + CHECK_INTERVAL))
done

# Check if we got the IPs
if [ -z "${VM_DEFAULT_IP}" ] || [ -z "${VM_IP}" ]; then
  echo "ERROR: Failed to get VM IPs within ${TIMEOUT_SECONDS} seconds"
  echo "VM DEFAULT IP: ${VM_DEFAULT_IP:-not available}"
  echo "VM IP: ${VM_IP:-not available}"
  exit 1
fi

# Configure the VM
echo "Provisioning the VM..."

# Executing commands
echo "Executing commands in the VM..."

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${USER}@${VM_DEFAULT_IP} <<EOF

  # Install necessary packages
  sudo dnf install -y epel-release libvirt libvirt-client virt-install pam-devel swtpm wget \
                    make golang git \
                    podman qemu-kvm sshpass skopeo
  sudo dnf --enablerepo=crb install -y libvirt-devel

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

# Clean up
echo "Cleaning up stuff..."
rm ${USER_DATA_FILE}

# Greetings
echo "You can access the created VM with ssh ${USER}@${VM_IP}"
echo "VM creation and provisioning complete!"

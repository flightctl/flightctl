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
VM_DISK_SIZE=30          # Disk size
NETWORK_NAME="$(get_ocp_nodes_network)"   # Network name
NETWORK_NAME=${NETWORK_NAME:-baremetal-0}
echo "ocp_network name is: ${NETWORK_NAME}"
ISO_URL="https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-x86_64-9-latest.x86_64.qcow2"
DISK_PATH="/var/lib/libvirt/images/${VM_NAME}.qcow2"
DISK_PATH_SRC="/var/lib/libvirt/images/${VM_NAME}_src.qcow2"
VIRT_BRIDGE="virbr0"         # Default libvirt bridge
TIMEOUT_SECONDS=30
USER="kni"
USER_HOME="/home/${USER}"   # user $HOME in the vm
SSH_PRIVATE_KEY_PATH="/home/${USER}/.ssh/id_rsa"
SSH_PUBLIC_KEY_PATH="/home/${USER}/.ssh/id_rsa.pub"
USER_DATA_FILE="user-data.yaml"
FLIGHTCTL_PATH="/home/${USER}/flightctl"

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

# Resize the VM image
qemu-img resize ${DISK_PATH} +20G  # bumping for the increased number of agent-images to be saved

# Create the VM
echo "Creating virtual machine ${VM_NAME}..."
virt-install \
  --name $VM_NAME \
  --memory $VM_RAM \
  --vcpus $VM_CPUS \
  --disk path=$DISK_PATH,size=${VM_DISK_SIZE},format=qcow2 \
  --os-variant centos-stream9  \
  --network network="default" \
  --network network=$NETWORK_NAME,model=virtio \
  --import \
  --cpu host-model \
  --graphics none \
  --noautoconsole \
  --cloud-init disable=on,user-data=user-data.yaml

# Wait for the VM to start
echo "Waiting ${TIMEOUT_SECONDS} seconds for VM to start..."
sleep ${TIMEOUT_SECONDS}

# Configure the VM
echo "Provisioning the VM..."

# Get the VM IPs
export INTERFACE_DEFAULT=$(sudo virsh domiflist ${VM_NAME} | grep default | awk '{print $1}')
VM_DEFAULT_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_DEFAULT} | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
echo "VM DEFAULT IP: ${VM_DEFAULT_IP}"

export INTERFACE_BM=$(sudo virsh domiflist ${VM_NAME} | grep ${NETWORK_NAME} | awk '{print $1}')
VM_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_BM} | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
echo "VM IP: ${VM_IP}"

# Executing commands
echo "Executing commands in the VM..."

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${USER}@${VM_DEFAULT_IP} <<EOF

  # Install necessary packages
  sudo dnf install -y epel-release libvirt libvirt-client virt-install swtpm \
                    make golang git \
                    podman qemu-kvm sshpass
  sudo dnf --enablerepo=crb install -y libvirt-devel

  # Install OpenShift client
  echo "Installing OpenShift client..."
  curl https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/stable/openshift-client-linux-amd64-rhel9.tar.gz | sudo tar xvz -C /usr/local/bin

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

# Sync the necessary folders
echo "Setting up synced folders..."
scp -r -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${FLIGHTCTL_PATH} ${USER}@${VM_IP}:${USER_HOME}/

# Clean up
echo "Cleaning up stuff..."
rm ${USER_DATA_FILE}

# Greetings
echo "You can access the created VM with ssh ${USER}@${VM_IP}"
echo "VM creation and provisioning complete!"

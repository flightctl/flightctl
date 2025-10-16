#!/bin/bash

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

# Variables
VM_NAME=${VM_NAME:-"quadlets-vm"}
VM_RAM=10240                # RAM in MB necessary to run the flightctl e2e
VM_CPUS=8                  # Number of CPUs
VM_DISK_SIZE_INC=${VM_DISK_SIZE_INC:-30} # Disk size increment
echo "Disk size increment: ${VM_DISK_SIZE_INC}G"
QUAY_ISO_URL="quay.io/flightctl-tests/rhel:rhel-9.6"
DISK_PATH="/var/lib/libvirt/images/${VM_NAME}.qcow2"
DISK_PATH_SRC="/var/lib/libvirt/images/${VM_NAME}_src.qcow2"
VIRT_BRIDGE="virbr0"         # Default libvirt bridge
TIMEOUT_SECONDS=30
USER=${USER:-$(whoami)}
USER_HOME="/home/${USER}"   # user $HOME in the vm
SSH_PRIVATE_KEY_PATH=${SSH_PRIVATE_KEY_PATH:-"/home/${USER}/.ssh/id_rsa"}
SSH_PUBLIC_KEY_PATH=${SSH_PUBLIC_KEY_PATH:-"/home/${USER}/.ssh/id_rsa.pub"}
USER_DATA_FILE="user-data.yaml"
REMOTE_URL=${REMOTE_URL:-"https://github.com/flightctl/flightctl.git"}
RPM_COPR="$(copr_repo)"
RPM_COPR=${RPM_COPR:-"@redhat-et/flightctl-dev"}
RPM_PACKAGE="flightctl-services"
RPM_CLIENT="flightctl-cli"

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
    sudo podman create --name tempdisk ${QUAY_ISO_URL}
    sudo podman cp tempdisk:/disk.qcow2 ${DISK_PATH_SRC}
    sudo podman rm tempdisk
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

# Create the VM
echo "Creating virtual machine ${VM_NAME}..."
virt-install \
  --name $VM_NAME \
  --memory $VM_RAM \
  --vcpus $VM_CPUS \
  --disk path=$DISK_PATH,format=qcow2 \
  --os-variant centos-stream9  \
  --network network="default" \
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

# Get the VM IP
export INTERFACE_DEFAULT=$(sudo virsh domiflist ${VM_NAME} | grep default | awk '{print $1}')
VM_DEFAULT_IP=$(sudo virsh domifaddr ${VM_NAME} --interface ${INTERFACE_DEFAULT} | awk '/ipv4/ {print $4}' | cut -d'/' -f1)
echo "VM DEFAULT IP: ${VM_DEFAULT_IP}"


# Executing commands
echo "Executing commands in the VM..."

ssh -i ${SSH_PRIVATE_KEY_PATH} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${USER}@${VM_DEFAULT_IP} <<EOF

  # Install necessary packages
  sudo subscription-manager register --username $REDHAT_USER --password $REDHAT_PASSWORD
  sudo dnf install -y \
  https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm
  sudo dnf repolist
  sudo dnf install -y libvirt libvirt-client virt-install swtpm \
                    make golang git \
                    podman qemu-kvm sshpass
  sudo dnf --enablerepo=crb install -y libvirt-devel

  sudo dnf copr -y enable ${RPM_COPR}
  sudo dnf install -y ${RPM_PACKAGE} ${RPM_CLIENT}

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

  # Start flightctl services
  sudo systemctl start flightctl.target
  cd $USER_HOME/flightctl
  flightctl login https://localhost:3443 -k
EOF

# Clean up
echo "Cleaning up stuff..."
rm ${USER_DATA_FILE}

# Greetings
echo "You can access the created VM with ssh ${USER}@${VM_DEFAULT_IP}"
echo "VM creation and provisioning complete!"

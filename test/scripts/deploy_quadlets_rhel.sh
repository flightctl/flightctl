#!/bin/bash
#
# Deploy FlightCtl Quadlets VM Script
#
# Creates a RHEL VM and installs FlightCtl services using systemd quadlets.
# Supports installing from git version/branch, brew-build, or default COPR.
#
# Usage:
#   # Default: Install from default COPR repository
#   ./deploy_quadlets_rhel.sh
#
#   # Build and install from git tag/version
#   GIT_VERSION="v1.0.0" ./deploy_quadlets_rhel.sh
#
#   # Install from brew build (downloads inside VM)
#   BREW_BUILD_URL="<brew-url>?taskID=<task-id>" ./deploy_quadlets_rhel.sh
#
# Environment Variables:
#   GIT_VERSION         - Git tag/version to clone and build from inside VM (e.g., "v1.0.0")
#   BREW_BUILD_URL      - Full URL to brew build page (downloads inside VM)
#   VM_NAME             - VM name (default: "quadlets-vm")
#   VM_DISK_SIZE_INC    - Disk size increment in GB (default: 30)
#   USER                - Username for VM access (default: current user)
#   REDHAT_USER         - Red Hat account username
#   REDHAT_PASSWORD     - Red Hat account password

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

# Package installation configuration
# Priority order (determined by environment variables):
# 1. If GIT_VERSION is set, clone repo with that tag and build RPMs inside VM
# 2. If BREW_BUILD_URL is set, download brew builds inside VM
# 3. Otherwise, use default COPR repository

if [[ -n "${GIT_VERSION:-}" ]]; then
    echo "Using GIT_VERSION: ${GIT_VERSION} - will clone and build from source inside VM"
    INSTALL_METHOD="git-build"
elif [[ -n "${BREW_BUILD_URL:-}" ]]; then
    echo "Using BREW_BUILD_URL: ${BREW_BUILD_URL} - will download brew builds inside VM"
    INSTALL_METHOD="brew"
else
    echo "Using default COPR repository for package installation"
    INSTALL_METHOD="default"
    RPM_COPR="$(copr_repo)"
    RPM_COPR=${RPM_COPR:-"@redhat-et/flightctl-dev"}
    RPM_PACKAGE="flightctl-services"
    RPM_CLIENT="flightctl-cli"
    RPM_OBS="flightctl-observability"
    RPM_TELEMETRY="flightctl-telemetry-gateway"
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
   sudo dnf install -y libvirt libvirt-client virt-install pam-devel swtpm wget \
                     make golang git \
                     podman qemu-kvm sshpass
  sudo dnf --enablerepo=crb install -y libvirt-devel

  # Install build dependencies for git-build method
  if [[ "${INSTALL_METHOD}" == "git-build" ]]; then
    echo "Installing build dependencies for RPM building..."
    sudo dnf install -y packit rpm-build go-rpm-macros openssl-devel selinux-policy selinux-policy-devel
  fi

  # Build or download FlightCtl packages based on installation method
  if [[ "${INSTALL_METHOD}" == "git-build" ]]; then
    echo "Building FlightCtl packages from git version ${GIT_VERSION}..."
    BUILD_DIR="${USER_HOME}"
    cd \${BUILD_DIR}

    # Clone repository with the specified tag
    echo "Cloning flightctl repository with tag ${GIT_VERSION}..."
    echo "Repository will be cloned to: \${BUILD_DIR}/flightctl"
    git clone --depth 1 --branch "${GIT_VERSION}" "${REMOTE_URL}" flightctl || {
      echo "ERROR: Failed to clone repository with tag ${GIT_VERSION}"
      exit 1
    }

    echo "Repository cloned successfully to: \${BUILD_DIR}/flightctl"

    # Change to the flightctl repository directory before building
    cd \${BUILD_DIR}/flightctl
    echo "Current directory: \$(pwd)"

    # Build RPMs
    echo "Building RPMs..."
    make rpm || {
      echo "ERROR: Failed to build RPMs"
      exit 1
    }

    # Verify RPMs were built
    if ! ls bin/rpm/flightctl-*.rpm >/dev/null 2>&1; then
      echo "ERROR: No RPMs found in bin/rpm after build"
      exit 1
    fi

    echo "Successfully built RPMs:"
    ls -lh bin/rpm/flightctl-*.rpm

    # Install service RPMs (exclude agent, selinux, debug, src)
    cd bin/rpm
    echo "Installing FlightCtl packages from built RPMs in: \$(pwd)"
    # Filter out unwanted packages before installing
    INSTALL_RPMS=""
    for rpm in flightctl-*.rpm; do
      if [[ ! "\${rpm}" =~ (agent|selinux|debug|\.src\.rpm) ]]; then
        if [[ -f "\${rpm}" ]]; then
          INSTALL_RPMS="\${INSTALL_RPMS} \${rpm}"
        fi
      fi
    done

    if [[ -z "\${INSTALL_RPMS}" ]]; then
      echo "ERROR: No service RPMs found to install (all were filtered out)"
      echo "Available RPMs:"
      ls -lh flightctl-*.rpm 2>/dev/null || echo "No RPMs found"
      exit 1
    fi

    echo "Installing RPMs: \${INSTALL_RPMS}"
    # Install each RPM individually to handle missing packages gracefully
    INSTALL_FAILED=0
    for rpm in \${INSTALL_RPMS}; do
      if [[ -f "\${rpm}" ]]; then
        echo "Installing \${rpm}..."
        if sudo dnf install -y "\${rpm}"; then
          echo "Successfully installed \${rpm}"
        else
          echo "WARNING: Failed to install \${rpm} (may not be available in this version)"
          INSTALL_FAILED=1
        fi
      fi
    done

    # Check if at least the core services package was installed
    if ! rpm -q flightctl-services >/dev/null 2>&1; then
      echo "ERROR: flightctl-services package was not installed, which is required"
      exit 1
    fi

    if [[ \${INSTALL_FAILED} -eq 1 ]]; then
      echo "WARNING: Some packages failed to install (may not be available in version ${GIT_VERSION})"
      echo "Installed packages:"
      rpm -qa | grep flightctl || echo "No flightctl packages found"
    fi
    cd -

  elif [[ "${INSTALL_METHOD}" == "brew" ]]; then
    echo "Downloading FlightCtl packages from brew build: ${BREW_BUILD_URL}"

    # Login to Red Hat registry for container image access
    echo "Logging in to registry.redhat.io..."
    sudo podman login registry.redhat.io --username $REDHAT_USER --password $REDHAT_PASSWORD || {
      echo "WARNING: Failed to login to registry.redhat.io (container images may not be available)"
    }

    BREW_TMP_DIR="${USER_HOME}/brew-rpms"
    mkdir -p \${BREW_TMP_DIR}
    cd \${BREW_TMP_DIR}

    # Fetch brew page and extract RPM URLs
    brew_page=\$(curl -k -s "${BREW_BUILD_URL}")
    if [ \$? -ne 0 ]; then
      echo "ERROR: Failed to fetch brew page from URL: ${BREW_BUILD_URL}"
      exit 1
    fi

    # Extract RPM URLs (use sed as fallback if grep -P not available)
    rpm_urls=\$(echo "\${brew_page}" | grep -oP 'https://[^"]+\.rpm' 2>/dev/null || echo "\${brew_page}" | grep -o 'https://[^"]*\.rpm')
    if [[ -z "\${rpm_urls}" ]]; then
      echo "ERROR: No RPM URLs found in brew page"
      exit 1
    fi

    # Download required RPMs
    echo "Downloading RPMs from brew..."
    while IFS= read -r url; do
      [[ -z "\$url" ]] && continue
      filename=\$(basename "\$url")
      # Download service packages we need (exclude agent, selinux, debug, src)
      if [[ "\${filename}" =~ ^flightctl-(services|observability|telemetry-gateway)- ]] || \
         ([[ "\${filename}" =~ ^flightctl- ]] && \
          [[ ! "\${filename}" =~ (agent|selinux|debug|\.src\.rpm) ]]); then
        echo "Downloading \${filename}..."
        wget --no-check-certificate -O "\${filename}" "\$url" || exit 1
      fi
    done <<< "\${rpm_urls}"

    # Install downloaded RPMs
    if ls flightctl-*.rpm >/dev/null 2>&1; then
      echo "Installing FlightCtl packages from brew RPMs..."
      sudo dnf install -y flightctl-*.rpm || {
        echo "ERROR: Failed to install brew RPMs"
        exit 1
      }
    else
      echo "ERROR: No FlightCtl RPMs found in brew build"
      exit 1
    fi

    cd - > /dev/null

  else
    echo "Installing FlightCtl packages from COPR: ${RPM_COPR}"
    sudo dnf copr -y enable ${RPM_COPR}
    sudo dnf install -y ${RPM_PACKAGE} ${RPM_CLIENT} ${RPM_OBS} ${RPM_TELEMETRY}
  fi

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

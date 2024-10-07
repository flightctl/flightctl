# -*- mode: ruby -*-
# vi: set ft=ruby :

Vagrant.configure("2") do |config|
 
  config.vm.box = "https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-Vagrant-9-latest.x86_64.vagrant-libvirt.box"

  config.vm.provider :libvirt do |domain|
    domain.memory = 8192
    domain.machine_virtual_size = 30
    domain.cpus = 8
    # this will run the VM connected to the same network used testing with a virtual OpenShift in QE
    domain.management_network_name = "baremetal-0"
    # Enable KVM nested virtualization
    domain.nested = true
    domain.cpu_mode = "host-model"
  end

   config.vm.synced_folder ".", "/vagrant", type: "rsync",rsync__exclude: "./bin/"

  #config.vm.synced_folder ".", "/home/vagrant/flightctl", type: "nfs" , nfs_udp: false, map_uid: 1000, map_gid: 1000
  config.vm.synced_folder "~/.kube", "/home/vagrant/.kube", type: "rsync"

  # whole home sync, simpler for development, but id mapping not working...
  # config.vm.synced_folder ".", "/home/vagrant/flightctl", type: "nfs" , nfs_udp: false, linux__nfs_options: ['no_root_squash']

  config.vm.provision "shell", inline: <<-SHELL
    # delete and recreate vda1 partition to full disk size,
    # delete, new, primary, 1, default, default, no, write
    sudo fdisk /dev/vda <<EOF
d
n
p
1


n
w
EOF
    sudo resize2fs /dev/vda1

    sudo dnf install -y epel-release libvirt libvirt-client virt-install swtpm \
                    make golang git \
                    podman qemu-kvm sshpass
    sudo dnf --enablerepo=crb install -y libvirt-devel

    echo "Installing openshift client"
    curl https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/stable/openshift-client-linux-amd64-rhel9.tar.gz | sudo tar xvz -C /usr/local/bin

    sudo systemctl enable --now libvirtd

    echo "Configuring Kind for rootless operation in Linux"
    # Enable rootless Kind, see https://kind.sigs.k8s.io/docs/user/rootless/
    sudo mkdir -p /etc/systemd/system/user@.service.d
    cat <<EOF | sudo tee /etc/systemd/system/user@.service.d/delegate.conf > /dev/null
[Service]
Delegate=yes
EOF
  SHELL
end

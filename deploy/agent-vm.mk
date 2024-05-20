VMNAME ?= flightctl-device-default
VMRAM ?= 512
VMCPUS ?= 1
VMDISK = /var/lib/libvirt/images/$(VMNAME).qcow2
VMWAIT ?= 0
IMAGE ?= localhost/local-flightctl-agent:latest

bin/output/qcow2/disk.qcow2: hack/Containerfile.local bin/.rpm
	sudo podman build -f hack/Containerfile.local -t localhost/local-flightctl-agent:latest .
	mkdir -p bin/output
	sudo podman run --rm \
					-it \
					--privileged \
					--pull=newer \
					--security-opt label=type:unconfined_t \
	                -v $(shell pwd)/bin/output:/output \
					-v /var/lib/containers/storage:/var/lib/containers/storage \
					quay.io/centos-bootc/bootc-image-builder:latest \
					--type qcow2 \
					--local $(IMAGE)

agent-image: bin/output/qcow2/disk.qcow2
	@echo "Agent image built at bin/output/qcow2/disk.qcow2"
	sudo chmod a+rw $(VMDISK) 2>/dev/null || true

.PHONY: agent-image

# This target is used to build the base image for the agent upgrade
agent-upgrade-image:
	# start local registry
	sudo mkdir -p /var/lib/registry
	sudo podman container run -dt -p 5000:5000 --name registry --volume registry:/var/lib/registry:Z docker.io/library/registry:2
	# build base agent image
	sudo podman build -f hack/Containerfile.upgrade -t localhost/local-flightctl-agent:upgrade .
	# build image to upgrade to push to local registry
	sudo podman build -f hack/Containerfile.local -t localhost:5000/local-flightctl-agent:latest .
	sudo podman push localhost:5000/local-flightctl-agent:latest --tls-verify=false
	$(MAKE) agent-image IMAGE=localhost/local-flightctl-agent:upgrade
	@echo "Base upgrade base agent image built at bin/output/qcow2/disk.qcow2"
	sudo chmod a+rw $(VMDISK) 2>/dev/null || true

.PHONY: agent-upgrade-image

agent-vm: bin/output/qcow2/disk.qcow2
	@echo "Booting Agent VM from $(VMDISK)"
	sudo cp bin/output/qcow2/disk.qcow2 $(VMDISK)
	sudo chown libvirt:libvirt $(VMDISK) 2>/dev/null || true
	sudo virt-install --name $(VMNAME) \
					  --vcpus $(VMCPUS) \
					  --memory $(VMRAM) \
					  --import --disk $(VMDISK),format=qcow2 \
					  --os-variant fedora-eln  \
					  --autoconsole text \
					  --wait $(VMWAIT) \
					  --transient || true

agent-vm-console:
	sudo virsh console $(VMNAME)

.PHONY: agent-vm

clean-agent-vm:
	sudo virsh destroy $(VMNAME) || true
	sudo rm -f $(VMDISK)

.PHONY: clean-agent-vm
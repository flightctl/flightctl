VMNAME ?= flightctl-device-default
VMRAM ?= 512
VMCPUS ?= 1
VMDISK = /var/lib/libvirt/images/$(VMNAME).qcow2
VMWAIT ?= 0

bin/output/disk.qcow2: hack/Containerfile.local rpm
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
					--local localhost/local-flightctl-agent:latest

agent-image: bin/output/disk.qcow2
	@echo "Agent image built at bin/output/disk.qcow2"

.PHONY: agent-image

agent-vm: bin/output/qcow2/disk.qcow2
	@echo "Booting Agent VM from $(VMDISK)"
	sudo cp bin/output/qcow2/disk.qcow2 $(VMDISK)
	sudo chown qemu:qemu $(VMDISK)
	sudo virt-install --name $(VMNAME) \
					  --vcpus $(VMCPUS) \
					  --memory $(VMRAM) \
					  --import --disk $(VMDISK),format=qcow2 \
					  --os-variant fedora-eln  \
					  --console pty,target_type=serial  \
					  --wait $(VMWAIT) \
					  --transient

agent-vm-console:
	sudo virsh console $(VMNAME)

.PHONY: agent-vm

clean-agent-vm:
	sudo virsh destroy $(VMNAME) || true
	sudo rm -f $(VMDISK)

.PHONY: clean-agent-vm
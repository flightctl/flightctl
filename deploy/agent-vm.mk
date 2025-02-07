VMNAME ?= flightctl-device-default
VMRAM ?= 512
VMCPUS ?= 1
VMDISK = /var/lib/libvirt/images/$(VMNAME).qcow2
VMWAIT ?= 0
CONTAINER_NAME ?= flightctl-device-no-bootc:base

BUILD_TYPE := bootc

agent-vm: bin/output/qcow2/disk.qcow2
	@echo "Booting Agent VM from $(VMDISK)"
	sudo cp bin/output/qcow2/disk.qcow2 $(VMDISK)
	sudo chown libvirt:libvirt $(VMDISK) 2>/dev/null || true
	sudo virt-install --name $(VMNAME) \
		--tpm backend.type=emulator,backend.version=2.0,model=tpm-tis \
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

agent-container: BUILD_TYPE := regular
agent-container: bin/output/qcow2/disk.qcow2
	@echo "Starting Agent Container flightctl-agent from $(CONTAINER_NAME)"
	sudo podman run -d --network host --name flightctl-agent localhost:5000/$(CONTAINER_NAME)

run-agent-container:
	sudo podman run -d --network host -v ./bin/flightctl-agent:/usr/bin/flightctl-agent:Z --name flightctl-agent localhost:5000/$(CONTAINER_NAME)

clean-agent-container:
	sudo podman stop flightctl-agent || true
	sudo podman rm flightctl-agent || true

.PHONY: agent-container run-agent-container clean-agent-container

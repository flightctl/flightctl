VMNAME ?= flightctl-device-default
VMRAM ?= 512
VMCPUS ?= 1
VMDISK = /var/lib/libvirt/images/$(VMNAME).qcow2
VMWAIT ?= 0
CONTAINER_NAME ?= flightctl-device-no-bootc:base
AGENT_IP ?= 192.168.122.6

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


update-vm-agent: bin/flightctl-agent
	@echo "Updating Agent VM $(AGENT_IP) with new flightctl-agent, if asked the password is 'user'"
	ssh-copy-id user@$(AGENT_IP)
	scp bin/flightctl-agent user@$(AGENT_IP):~
	ssh user@$(AGENT_IP) "sudo ostree admin unlock || true"
	ssh user@$(AGENT_IP) "sudo mv /home/user/flightctl-agent /usr/bin/flightctl-agent"
	ssh user@$(AGENT_IP) "sudo systemctl restart flightctl-agent"
	ssh user@$(AGENT_IP) "sudo journalctl -u flightctl-agent -f"

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
	podman run -d --name flightctl-agent localhost:5000/"$(CONTAINER_NAME)"

clean-agent-container:
	podman stop flightctl-agent
	podman rm flightctl-agent

.PHONY: agent-container

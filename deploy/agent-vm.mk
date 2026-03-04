VMNAME ?= flightctl-device-default
VMRAM ?= 2048
VMCPUS ?= 1
VMDISK = /var/lib/libvirt/images/$(VMNAME).qcow2
VMDISKSIZE_DEFAULT := 10G
VMDISKSIZE ?= $(VMDISKSIZE_DEFAULT)
VMWAIT ?= 0
CONTAINER_NAME ?= flightctl-device-no-bootc:base
INJECT_CONFIG ?= true

BUILD_TYPE := bootc

define SWTPM_CONFIG
statedir = /var/lib/flightctl-swtpm-test-ca
signingkey = /var/lib/flightctl-swtpm-test-ca/signkey.pem
issuercert = /var/lib/flightctl-swtpm-test-ca/issuercert.pem
certserial = /var/lib/flightctl-swtpm-test-ca/certserial
endef
export SWTPM_CONFIG

ifeq ($(INJECT_CONFIG),true)
agent-vm: bin/output/qcow2/disk.qcow2 prepare-e2e-qcow-config
else
agent-vm: bin/output/qcow2/disk.qcow2
endif
ifeq ($(TPM),enabled)
	@echo "Configuring swtpm to use test CA from bin/swtpm-ca/..."
	@if [ ! -f bin/swtpm-ca/signkey.pem ]; then \
		echo "ERROR: Test swtpm CA not found. Run 'TPM=enabled make deploy' first."; \
		exit 1; \
	fi
	@# Copy swtpm test CA to system location accessible by tss user (swtpm needs write access to statedir)
	@echo "Installing swtpm test CA to /var/lib/flightctl-swtpm-test-ca/..."
	@sudo mkdir -p /var/lib/flightctl-swtpm-test-ca
	@sudo cp bin/swtpm-ca/signkey.pem bin/swtpm-ca/issuercert.pem bin/swtpm-ca/certserial bin/swtpm-ca/swtpm-localca-rootca-cert.pem /var/lib/flightctl-swtpm-test-ca/
	@sudo chown -R tss:tss /var/lib/flightctl-swtpm-test-ca
	@sudo chmod -R u=rwX,go=rX /var/lib/flightctl-swtpm-test-ca
	@echo "✓ Test CA installed to /var/lib/flightctl-swtpm-test-ca/ (owned by tss:tss)"
	@# Create marker file to track that we installed this configuration
	@sudo touch /var/lib/flightctl-swtpm-installed.marker
	@# Temporarily install system-wide swtpm-localca config pointing to test CA
	@echo "Installing temporary system-wide swtpm configuration..."
	@if [ -f /etc/swtpm-localca.conf ]; then \
		sudo cp /etc/swtpm-localca.conf /etc/swtpm-localca.conf.backup; \
		echo "  (Backed up existing /etc/swtpm-localca.conf to /etc/swtpm-localca.conf.backup)"; \
	fi
	@echo "$$SWTPM_CONFIG" | sudo tee /etc/swtpm-localca.conf > /dev/null
	@# Ensure tss user can read the config file (owned by root)
	@sudo chmod 644 /etc/swtpm-localca.conf
	@echo "✓ Installed /etc/swtpm-localca.conf (will be restored on clean)"
	@echo "⚠️  WARNING: System-wide swtpm configuration temporarily points to swtpm test CA"
	@echo "⚠️  Starting VM with swtpm TEST CA (for testing only!)"
endif
	@echo "Booting Agent VM from $(VMDISK) with disk size $(VMDISKSIZE)"
	sudo cp bin/output/qcow2/disk.qcow2 $(VMDISK)
	@if [ "$(VMDISKSIZE)" != "$(VMDISKSIZE_DEFAULT)" ]; then \
		sudo qemu-img resize $(VMDISK) $(VMDISKSIZE); \
	fi
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
	@AGENT_IP=$$(sudo virsh domifaddr $(VMNAME) | awk '/ipv4/ {print $$4}' | cut -d'/' -f1 | head -n1); \
	if [ -z "$$AGENT_IP" ]; then \
		echo "ERROR: VM $(VMNAME) not running or no IP found"; \
		exit 1; \
	fi; \
	echo "Updating Agent VM $$AGENT_IP with new flightctl-agent, if asked the password is 'user'"; \
	ssh-copy-id -o IdentitiesOnly=yes user@$$AGENT_IP; \
	scp -o IdentitiesOnly=yes bin/flightctl-agent user@$$AGENT_IP:~; \
	ssh -o IdentitiesOnly=yes user@$$AGENT_IP "sudo ostree admin unlock || true"; \
	ssh -o IdentitiesOnly=yes user@$$AGENT_IP "sudo mv /home/user/flightctl-agent /usr/bin/flightctl-agent"; \
	ssh -o IdentitiesOnly=yes user@$$AGENT_IP "sudo restorecon /usr/bin/flightctl-agent"; \
	ssh -o IdentitiesOnly=yes user@$$AGENT_IP "sudo systemctl restart flightctl-agent"; \
	ssh -o IdentitiesOnly=yes user@$$AGENT_IP "sudo journalctl -u flightctl-agent -f"

agent-vm-console:
	sudo virsh console $(VMNAME)

.PHONY: agent-vm

clean-agent-vm:
	sudo virsh destroy $(VMNAME) || true
	sudo rm -f $(VMDISK)
	@# Restore original swtpm-localca configuration only if we installed it
	@if [ -f /var/lib/flightctl-swtpm-installed.marker ]; then \
		if [ -f /etc/swtpm-localca.conf.backup ]; then \
			echo "Restoring original /etc/swtpm-localca.conf..."; \
			sudo mv /etc/swtpm-localca.conf.backup /etc/swtpm-localca.conf; \
		elif [ -f /etc/swtpm-localca.conf ]; then \
			echo "Removing temporary /etc/swtpm-localca.conf..."; \
			sudo rm /etc/swtpm-localca.conf; \
		fi; \
		if [ -d /var/lib/flightctl-swtpm-test-ca ]; then \
			echo "Removing test CA from /var/lib/flightctl-swtpm-test-ca/..."; \
			sudo rm -rf /var/lib/flightctl-swtpm-test-ca; \
		fi; \
		echo "Removing flightctl swtpm marker..."; \
		sudo rm -f /var/lib/flightctl-swtpm-installed.marker; \
	fi

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

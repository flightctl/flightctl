VMNAME    ?= flightctl-device-default
VMRAM     ?= 2048
VMSSHPORT ?= 2222
INJECT_CONFIG ?= true

BUILD_TYPE := bootc

ifeq ($(INJECT_CONFIG),true)
agent-vm: bin/flightctl-dev-vm bin/output/qcow2/disk.qcow2 prepare-e2e-qcow-config
else
agent-vm: bin/flightctl-dev-vm bin/output/qcow2/disk.qcow2
endif
	bin/flightctl-dev-vm start --name $(VMNAME) --mem $(VMRAM) --ssh-port $(VMSSHPORT)

update-vm-agent: bin/flightctl-agent
	@echo "Updating Agent VM at 127.0.0.1:$(VMSSHPORT) with new flightctl-agent"
	sshpass -p user scp -P $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null bin/flightctl-agent user@127.0.0.1:~
	sshpass -p user ssh -p $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@127.0.0.1 "sudo ostree admin unlock || true"
	sshpass -p user ssh -p $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@127.0.0.1 "sudo mv /home/user/flightctl-agent /usr/bin/flightctl-agent"
	sshpass -p user ssh -p $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@127.0.0.1 "sudo restorecon /usr/bin/flightctl-agent"
	sshpass -p user ssh -p $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@127.0.0.1 "sudo systemctl restart flightctl-agent"
	sshpass -p user ssh -p $(VMSSHPORT) -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null user@127.0.0.1 "sudo journalctl -u flightctl-agent -f"

agent-vm-console:
	bin/flightctl-dev-vm console --name $(VMNAME)

.PHONY: agent-vm agent-vm-console update-vm-agent

clean-agent-vm:
	bin/flightctl-dev-vm delete --name $(VMNAME)

.PHONY: clean-agent-vm

agent-container: BUILD_TYPE := regular
agent-container: bin/output/qcow2/disk.qcow2
	@echo "Starting Agent Container flightctl-agent from $(CONTAINER_NAME)"
	sudo podman run -d --network host --name flightctl-agent localhost:5000/$(CONTAINER_NAME)

CONTAINER_NAME ?= flightctl-device-no-bootc:base

run-agent-container:
	sudo podman run -d --network host -v ./bin/flightctl-agent:/usr/bin/flightctl-agent:Z --name flightctl-agent localhost:5000/$(CONTAINER_NAME)

clean-agent-container:
	sudo podman stop flightctl-agent || true
	sudo podman rm flightctl-agent || true

.PHONY: agent-container run-agent-container clean-agent-container

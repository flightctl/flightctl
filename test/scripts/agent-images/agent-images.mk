
bin/output/qcow2/disk.qcow2: bin/.e2e-agent-images

# Touch file to track when qcow2 was last built
bin/.e2e-agent-images: rpm bin/flightctl-agent bin/e2e-certs packaging/selinux/flightctl_agent.te packaging/selinux/flightctl_agent.fc packaging/systemd/flightctl-agent.service
	./test/scripts/agent-images/prepare_agent_config.sh
	BUILD_TYPE=$(BUILD_TYPE) ./test/scripts/agent-images/create_agent_images.sh
	./test/scripts/agent-images/create_application_image.sh
	touch bin/.e2e-agent-images

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f bin/.e2e-agent-images
	rm -rf bin/dnf-cache
	rm -rf bin/osbuild-cache
	rm -rf bin/rpm


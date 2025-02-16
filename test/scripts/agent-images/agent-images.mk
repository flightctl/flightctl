
bin/output/qcow2/disk.qcow2: e2e-agent-images

e2e-agent-images: bin rpm bin/e2e-certs
	./test/scripts/agent-images/prepare_agent_config.sh
	BUILD_TYPE=$(BUILD_TYPE) ./test/scripts/agent-images/create_agent_images.sh
	./test/scripts/agent-images/create_application_image.sh

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f bin/.e2e-agent-images


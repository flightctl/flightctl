
bin/output/qcow2/disk.qcow2: e2e-agent-images

e2e-agent-images: bin rpm bin/e2e-certs
	./test/scripts/agent-images/create_agent_images.sh

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f out/.e2e-agent-images


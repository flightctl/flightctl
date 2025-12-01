bin/output/qcow2/disk.qcow2: bin/.e2e-agent-images

# Environment variables for agent image builds
AGENT_IMAGE_OUTPUT ?= push
AGENT_OS_ID ?= cs9-bootc

# Default to push, set to false only for bundle
AGENT_PUSH_IMAGES = true
ifeq ($(AGENT_IMAGE_OUTPUT),bundle)
    AGENT_PUSH_IMAGES = false
endif

bin/.e2e-agent-images: bin/.rpm bin/flightctl-agent
	BUILD_TYPE=$(BUILD_TYPE) BREW_BUILD_URL=$(BREW_BUILD_URL) SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
		FLAVORS=$(AGENT_OS_ID) PUSH_IMAGES=$(AGENT_PUSH_IMAGES) ./test/scripts/agent-images/create_agent_images.sh
	touch bin/.e2e-agent-images

bin/.e2e-agent-certs:
	./test/scripts/agent-images/prepare_agent_config.sh
	touch bin/.e2e-agent-certs

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f bin/.e2e-agent-images
	rm -f bin/.e2e-agent-certs
	rm -f bin/.e2e-agent-injected
	rm -rf bin/dnf-cache
	rm -rf bin/osbuild-cache
	rm -rf bin/rpm
	rm -rf bin/brew-rpm


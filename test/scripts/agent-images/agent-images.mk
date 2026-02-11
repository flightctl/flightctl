# Environment variables for agent image builds
AGENT_IMAGE_OUTPUT ?= push
AGENT_OS_ID ?= cs9-bootc
APP_BUNDLE := $(ROOT_DIR)/bin/app-images-bundle.tar
AGENT_BUNDLE_DIR := $(ROOT_DIR)/bin/agent-artifacts
AGENT_BUNDLE := $(AGENT_BUNDLE_DIR)/agent-images-bundle-$(AGENT_OS_ID).tar

bin/output/qcow2/disk.qcow2: $(E2E_AGENT_IMAGES_SENTINEL)

# Build + bundle artifacts (no push)
$(E2E_AGENT_IMAGES_SENTINEL): | bin
	@if [ ! -f "$(AGENT_BUNDLE)" ]; then \
		$(MAKE) bin/.rpm; \
		BUILD_TYPE=$(BUILD_TYPE) BREW_BUILD_URL=$(BREW_BUILD_URL) SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
			AGENT_OS_ID=$(AGENT_OS_ID) PUSH_IMAGES=false ARTIFACTS_OUTPUT_DIR=$(AGENT_BUNDLE_DIR) $(ROOT_DIR)/test/scripts/agent-images/create_agent_images.sh; \
	else \
		echo "Device bundle already exists at $(AGENT_BUNDLE)"; \
	fi
	@if [ ! -f "$(APP_BUNDLE)" ]; then \
		SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
			PUSH_IMAGES=false $(ROOT_DIR)/test/scripts/agent-images/create_application_image.sh; \
	else \
		echo "App bundle already exists at $(APP_BUNDLE)"; \
	fi
	touch $(E2E_AGENT_IMAGES_SENTINEL)

.PHONY: push-e2e-agent-images
push-e2e-agent-images: e2e-agent-images
	@if [ ! -f "$(AGENT_BUNDLE)" ]; then \
		echo "Agent bundle not found at $(AGENT_BUNDLE). Run 'make e2e-agent-images' first."; \
		exit 1; \
	fi
	$(ROOT_DIR)/test/scripts/agent-images/scripts/upload-images.sh "$(AGENT_BUNDLE)"
	@if [ ! -f "$(APP_BUNDLE)" ]; then \
		echo "App bundle not found at $(APP_BUNDLE). Run 'make e2e-agent-images' first."; \
		exit 1; \
	fi
	$(ROOT_DIR)/test/scripts/agent-images/scripts/upload-images.sh "$(APP_BUNDLE)"

bin/.e2e-agent-certs:
	./test/scripts/agent-images/prepare_agent_config.sh
	touch bin/.e2e-agent-certs

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f bin/.e2e-agent-images-*
	rm -f bin/.e2e-agent-certs
	rm -f bin/.e2e-agent-injected
	rm -rf bin/dnf-cache
	rm -rf bin/osbuild-cache
	rm -rf bin/rpm
	rm -rf bin/.rpm
	rm -rf bin/brew-rpm
	@echo "Cleaning e2e test images from regular podman context..."
	- podman rmi $$(podman images --filter "label=io.flightctl.e2e.component=app" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true
	- podman rmi $$(podman images --filter "label=io.flightctl.e2e.component=device" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true
	@echo "Cleaning e2e test images from root podman context..."
	- sudo podman rmi $$(sudo podman images --filter "label=io.flightctl.e2e.component=app" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true
	- sudo podman rmi $$(sudo podman images --filter "label=io.flightctl.e2e.component=device" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true
	@echo "Deleting e2e image archives..."
	- rm -rf bin/agent-artifacts/ || true
	- rm -f bin/app-images-bundle.tar || true
	@echo "E2E image cleanup completed."

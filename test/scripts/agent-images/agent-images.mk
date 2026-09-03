# Environment variables for agent image builds
AGENT_IMAGE_OUTPUT ?= push
AGENT_OS_ID ?= cs9-bootc
APP_BUNDLE := $(ROOT_DIR)/bin/app-images-bundle.tar
AGENT_BUNDLE_DIR := $(ROOT_DIR)/bin/agent-artifacts
AGENT_BUNDLE := $(AGENT_BUNDLE_DIR)/agent-images-bundle-$(AGENT_OS_ID).tar

# Flavor guard for the shared qcow path.
# bin/output/qcow2/disk.qcow2 is a single path reused by every OS flavor, but
# each flavor has its own build sentinel. A disk left behind by a build of a
# different flavor makes THIS flavor's sentinel target look up to date, so make
# would skip the rebuild and the wrong image would boot (e.g. a cs9 disk for a
# Fedora WiFi run, silently skipping every WiFi spec). Both build paths record
# the disk's flavor in the disk.qcow2.os-id sidecar; if it does not match the
# requested AGENT_OS_ID, drop the disk and this flavor's sentinel here (at parse
# time) so the recipe below rebuilds from scratch. E2E_AGENT_IMAGES_SENTINEL is
# used verbatim (not recomputed) so the exact file make keys on is the one
# removed. The disk is user-owned after every successful build (both paths chown
# bin/output back), so no sudo is needed; a stale root-owned disk from an aborted
# build is still overwritten by the rebuild's mv into the user-owned directory.
QCOW2_DISK := $(ROOT_DIR)/bin/output/qcow2/disk.qcow2
QCOW2_OSID_FILE := $(QCOW2_DISK).os-id
QCOW2_RECORDED_OSID := $(strip $(if $(wildcard $(QCOW2_OSID_FILE)),$(shell cat $(QCOW2_OSID_FILE) 2>/dev/null)))
ifneq ($(QCOW2_RECORDED_OSID),)
ifneq ($(QCOW2_RECORDED_OSID),$(AGENT_OS_ID))
$(shell rm -f $(QCOW2_DISK) $(E2E_AGENT_IMAGES_SENTINEL))
endif
endif

bin/output/qcow2/disk.qcow2: $(E2E_AGENT_IMAGES_SENTINEL)

ifeq ($(AGENT_OS_ID),fedora-bootc)
# Fedora onboarding flavor: the onboarding suite (GINKGO_LABEL_FILTER=onboarding)
# needs a WiFi-capable device image (mac80211_hwsim baked; unobtainable on
# cs9/cs10). It uses neither the v2..v12 variants, the agent multi-image bundle,
# nor the app bundle, so this path builds only the base image and the qcow2 via a
# dedicated minimal orchestrator. Selected with AGENT_OS_ID=fedora-bootc, e.g.
#   AGENT_OS_ID=fedora-bootc make prepare-e2e-test
$(E2E_AGENT_IMAGES_SENTINEL): | bin
	SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
		$(ROOT_DIR)/test/scripts/agent-images/build_onboarding_image.sh
	touch $(E2E_AGENT_IMAGES_SENTINEL)
else
# Build + bundle artifacts (no push)
$(E2E_AGENT_IMAGES_SENTINEL): | bin
	@if [ ! -f "$(AGENT_BUNDLE)" ]; then \
		$(MAKE) bin/.rpm; \
		BREW_BUILD_URL=$(BREW_BUILD_URL) SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
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
endif

# Convenience alias: build the Fedora onboarding device image + qcow2 directly,
# regardless of the current AGENT_OS_ID. Equivalent to the fedora-bootc sentinel
# path above.
.PHONY: e2e-agent-image-onboarding
e2e-agent-image-onboarding: | bin
	SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
		$(ROOT_DIR)/test/scripts/agent-images/build_onboarding_image.sh

# Starts (or reuses) the e2e registry and uploads bundles via the same Go path
# as test runtime (auxiliary.StartServices → UploadImages).
.PHONY: push-e2e-agent-images
push-e2e-agent-images: e2e-agent-images
	@if [ ! -f "$(AGENT_BUNDLE)" ]; then \
		echo "Agent bundle not found at $(AGENT_BUNDLE). Run 'make e2e-agent-images' first."; \
		exit 1; \
	fi
	@if [ ! -f "$(APP_BUNDLE)" ]; then \
		echo "App bundle not found at $(APP_BUNDLE). Run 'make e2e-agent-images' first."; \
		exit 1; \
	fi
	go run ./test/e2e/infra/cmd/push-e2e-images

bin/.e2e-agent-certs:
	# Short enrollment-verify interval for e2e speed; wider Cap/Steps so that short
	# interval cannot exhaust the backoff during pristine VM-pool bootstrap.
	./test/scripts/agent-images/prepare_agent_config.sh \
		--enrollment-verify-interval 0m2s \
		--enrollment-verify-cap 0m90s \
		--enrollment-verify-steps 11
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

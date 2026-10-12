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
# the disk's flavor in the disk.qcow2.os-id sidecar. Force the matching sentinel
# recipe to run when the shared disk is missing or its recorded flavor differs;
# defer all writes until after the rootless preflight instead of deleting files
# while Make parses this file. Builds use the effective user's Podman store and
# keep generated files owned by that user. A root invocation uses root's store
# and rootful BIB.
QCOW2_DISK := $(ROOT_DIR)/bin/output/qcow2/disk.qcow2
QCOW2_OSID_FILE := $(QCOW2_DISK).os-id
QCOW2_RECORDED_OSID := $(strip $(if $(wildcard $(QCOW2_OSID_FILE)),$(shell cat $(QCOW2_OSID_FILE) 2>/dev/null)))
QCOW2_REBUILD_REQUIRED :=
ifeq ($(wildcard $(QCOW2_DISK)),)
QCOW2_REBUILD_REQUIRED := true
else ifneq ($(QCOW2_RECORDED_OSID),$(AGENT_OS_ID))
QCOW2_REBUILD_REQUIRED := true
endif

ifeq ($(QCOW2_REBUILD_REQUIRED),true)
.PHONY: force-e2e-agent-images-$(AGENT_OS_ID)
force-e2e-agent-images-$(AGENT_OS_ID):
$(E2E_AGENT_IMAGES_SENTINEL): force-e2e-agent-images-$(AGENT_OS_ID)
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
	RPM_MOCK_ROOT="$(RPM_MOCK_ROOT)" test/scripts/runtime_preflight.sh e2e-prepare
	SOURCE_GIT_TAG=$(SOURCE_GIT_TAG) SOURCE_GIT_TREE_STATE=$(SOURCE_GIT_TREE_STATE) SOURCE_GIT_COMMIT=$(SOURCE_GIT_COMMIT) \
		$(ROOT_DIR)/test/scripts/agent-images/build_onboarding_image.sh
	touch $(E2E_AGENT_IMAGES_SENTINEL)
else
# Build + bundle artifacts (no push)
$(E2E_AGENT_IMAGES_SENTINEL): | bin
	RPM_MOCK_ROOT="$(RPM_MOCK_ROOT)" test/scripts/runtime_preflight.sh e2e-prepare
	@set -e; \
	if [ "$(QCOW2_REBUILD_REQUIRED)" = "true" ] || [ ! -f "$(AGENT_BUNDLE)" ]; then \
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
	RPM_MOCK_ROOT="$(RPM_MOCK_ROOT)" test/scripts/runtime_preflight.sh e2e-prepare
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

.PHONY: e2e-agent-images clean-e2e-agent-images

clean-e2e-agent-images:
	test/scripts/runtime_preflight.sh clean
	@uid=$$(id -u); \
	if [ "$$uid" -eq 0 ]; then echo "Cleaning E2E artifacts and images from root's Podman store..."; else echo "Cleaning E2E artifacts and images from the current user's Podman store..."; fi; \
	config_home="$${XDG_CONFIG_HOME:-$$HOME/.config}"; \
	mock_root="$(if $(RPM_MOCK_ROOT),$(RPM_MOCK_ROOT),$(RPM_MOCK_ROOT_DEFAULT))"; \
	if [ "$$uid" -eq 0 ]; then registry_config=/etc/containers/registries.conf.d/flightctl-e2e.conf; else registry_config="$$config_home/containers/registries.conf.d/flightctl-e2e.conf"; fi; \
	if [ -f "$$registry_config" ] && head -n 1 "$$registry_config" | grep -q '^# Managed by Flight Control E2E;'; then \
		registry_owner=$$(find "$$registry_config" -maxdepth 0 -uid "$$uid" -print -quit 2>/dev/null); \
		find_status=$$?; \
		if [ "$$find_status" -ne 0 ]; then \
			echo "Leaving $$registry_config because its ownership could not be checked; inspect it as the owning user." >&2; \
		elif [ -n "$$registry_owner" ]; then \
			rm -f -- "$$registry_config"; \
		else \
			echo "Leaving $$registry_config because it is not owned by uid $$uid; clean it as the owning user." >&2; \
		fi; \
	fi; \
	remove_owned_path() { \
		path="$$1"; \
		[ -e "$$path" ] || [ -L "$$path" ] || return 0; \
		other_owner=$$(find "$$path" -xdev ! -uid "$$uid" -print -quit 2>/dev/null); \
		find_status=$$?; \
		if [ "$$find_status" -ne 0 ]; then \
			echo "Leaving $$path because its ownership could not be checked; inspect it as the owning user." >&2; \
		elif [ -n "$$other_owner" ]; then \
			echo "Leaving $$path because it contains files not owned by uid $$uid; clean it as the owning user." >&2; \
		else \
			find "$$path" -xdev -depth -delete || echo "Warning: failed to remove $$path without crossing filesystem boundaries" >&2; \
		fi; \
	}; \
	for path in bin/output/qcow2/disk.qcow2 bin/output/qcow2/disk.qcow2.os-id bin/.e2e-agent-images-* bin/.e2e-agent-certs bin/.e2e-agent-injected bin/rootless-bib-cache dnf-cache osbuild-cache bin/rpm bin/.rpm bin/brew-rpm bin/agent-artifacts bin/app-images-bundle.tar bin/output/agent-qcow2-* "mock-$$mock_root"; do remove_owned_path "$$path"; done; \
	podman rmi $$(podman images --filter "label=io.flightctl.e2e.component=app" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true; \
	podman rmi $$(podman images --filter "label=io.flightctl.e2e.component=device" --format "{{.Repository}}:{{.Tag}}" 2>/dev/null) 2>/dev/null || true
	@echo "E2E image cleanup completed."

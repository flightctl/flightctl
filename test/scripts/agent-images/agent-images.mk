
bin/output/qcow2/disk.qcow2: bin/.e2e-agent-images

bin/.e2e-agent-images: deploy-e2e-extras rpm bin/flightctl-agent bin/e2e-certs
	./test/scripts/agent-images/prepare_agent_config.sh
	BUILD_TYPE=$(BUILD_TYPE) BREW_BUILD_URL=$(BREW_BUILD_URL) ./test/scripts/agent-images/create_agent_images.sh
	./test/scripts/agent-images/create_application_image.sh
	touch bin/.e2e-agent-images

.PHONY: e2e-artifacts-offline
e2e-artifacts-offline: rpm bin/e2e-certs/ca.pem
	# Build seed agent images and seed qcow2 without cluster deps
	BUILD_PHASE=seed ./test/scripts/agent-images/create_agent_images.sh
	# Save seed images as OCI archives for caching in CI
	mkdir -p bin/output/oci
	for i in base v2 v3 v4 v5 v6 v8 v9 v10; do \
	  if podman image exists localhost:5000/flightctl-device-seed:$$i; then \
	    rm -f bin/output/oci/flightctl-device-seed-$$i.tar; \
	    podman save localhost:5000/flightctl-device-seed:$$i -o bin/output/oci/flightctl-device-seed-$$i.tar; \
	  fi; \
	done

.PHONY: e2e-artifacts-finalize
e2e-artifacts-finalize: bin/flightctl bin/e2e-certs/ca.pem
	# Generate agent enrollment config/certs against running backend
	./test/scripts/agent-images/prepare_agent_config.sh
	# Build finalize layer per variant and tag/push final images
	REGISTRY_ADDRESS=$$(test/scripts/functions registry_address) \
	./test/scripts/agent-images/finalize_agent_images.sh

.PHONY: e2e-agent-images

clean-e2e-agent-images:
	sudo rm -f bin/output/qcow2/disk.qcow2
	rm -f bin/.e2e-agent-images
	rm -rf bin/dnf-cache
	rm -rf bin/osbuild-cache
	rm -rf bin/rpm
	rm -rf bin/brew-rpm


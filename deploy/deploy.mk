ifeq ($(DB_SIZE),)
	DB_SIZE := e2e
endif

ifeq ($(STATUS_UPDATE_INTERVAL),)
	STATUS_UPDATE_INTERVAL := 0m2s
endif

ifeq ($(SPEC_FETCH_INTERVAL),)
	SPEC_FETCH_INTERVAL := 0m2s
endif

# Create kind cluster if it doesn't exist (idempotent)
cluster: bin/e2e-certs/ca.pem
	test/scripts/install_kind.sh
	kind get clusters | grep kind || test/scripts/create_cluster.sh

clean-cluster:
	kind delete cluster

deploy: FLAVOR ?= el9
ifndef SKIP_BUILD
deploy: cluster build-containers build-cli deploy-helm prepare-agent-config
else
deploy: cluster deploy-helm prepare-agent-config
	@echo "Skipping container and CLI builds (SKIP_BUILD is set)"
endif

redeploy-api: FLAVOR ?= el9
redeploy-api: build-containers
	test/scripts/redeploy.sh api

redeploy-worker: FLAVOR ?= el9
redeploy-worker: build-containers
	test/scripts/redeploy.sh worker

redeploy-periodic: FLAVOR ?= el9
redeploy-periodic: build-containers
	test/scripts/redeploy.sh periodic

redeploy-alert-exporter: FLAVOR ?= el9
redeploy-alert-exporter: build-containers
	test/scripts/redeploy.sh alert-exporter

redeploy-alertmanager-proxy: FLAVOR ?= el9
redeploy-alertmanager-proxy: build-containers
	test/scripts/redeploy.sh alertmanager-proxy

redeploy-telemetry-gateway: FLAVOR ?= el9
redeploy-telemetry-gateway: build-containers
	test/scripts/redeploy.sh telemetry-gateway

redeploy-imagebuilder-worker: FLAVOR ?= el9
redeploy-imagebuilder-worker: build-containers
	test/scripts/redeploy.sh imagebuilder-worker

redeploy-imagebuilder-api: flightctl-imagebuilder-api-container
	test/scripts/redeploy.sh imagebuilder-api

deploy-helm: FLAVOR ?= el9
ifndef SKIP_BUILD
deploy-helm: build-containers-ci build-cli
endif
deploy-helm:
	@echo "Deploying helm charts with FLAVOR=$(FLAVOR)"
	FLAVOR=$(FLAVOR) make generate
	kubectl config set-context kind-kind
	test/scripts/install_helm.sh
	test/scripts/deploy_with_helm.sh --db-size $(DB_SIZE)

prepare-agent-config:
	test/scripts/agent-images/prepare_agent_config.sh --status-update-interval $(STATUS_UPDATE_INTERVAL) --spec-fetch-interval $(SPEC_FETCH_INTERVAL)

deploy-db-helm: cluster
	test/scripts/deploy_with_helm.sh --only-db

deploy-db:
	sudo -E deploy/scripts/deploy_quadlet_service.sh db

deploy-kv:
	sudo -E deploy/scripts/deploy_quadlet_service.sh kv

deploy-alertmanager:
	sudo -E deploy/scripts/deploy_quadlet_service.sh alertmanager

deploy-alertmanager-proxy:
	sudo -E deploy/scripts/deploy_quadlet_service.sh alertmanager-proxy

# Can set the SKIP_BUILD variable to skip the build step and use existing containers
deploy-quadlets: FLAVOR ?= el9
deploy-quadlets:
ifndef SKIP_BUILD
	$(MAKE) build-containers
	@echo "Copying containers from user to root context for systemd services (FLAVOR=$(FLAVOR))..."
	# Check if flavor-specific images exist (new system) or fall back to old naming
	@if podman image exists flightctl-api-$(FLAVOR):latest; then \
		echo "Using flavor-specific container images..."; \
		podman save flightctl-api-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-db-setup-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-worker-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-periodic-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-alert-exporter-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-cli-artifacts-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-alertmanager-proxy-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-pam-issuer-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-imagebuilder-api-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-imagebuilder-worker-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-userinfo-proxy-$(FLAVOR):latest | sudo podman load; \
		podman save flightctl-telemetry-gateway-$(FLAVOR):latest | sudo podman load; \
		echo "Creating local alias tags for quadlet compatibility..."; \
		sudo podman tag flightctl-api-$(FLAVOR):latest flightctl-api:latest; \
		sudo podman tag flightctl-db-setup-$(FLAVOR):latest flightctl-db-setup:latest; \
		sudo podman tag flightctl-worker-$(FLAVOR):latest flightctl-worker:latest; \
		sudo podman tag flightctl-periodic-$(FLAVOR):latest flightctl-periodic:latest; \
		sudo podman tag flightctl-alert-exporter-$(FLAVOR):latest flightctl-alert-exporter:latest; \
		sudo podman tag flightctl-cli-artifacts-$(FLAVOR):latest flightctl-cli-artifacts:latest; \
		sudo podman tag flightctl-alertmanager-proxy-$(FLAVOR):latest flightctl-alertmanager-proxy:latest; \
		sudo podman tag flightctl-pam-issuer-$(FLAVOR):latest flightctl-pam-issuer:latest; \
		sudo podman tag flightctl-imagebuilder-api-$(FLAVOR):latest flightctl-imagebuilder-api:latest; \
		sudo podman tag flightctl-imagebuilder-worker-$(FLAVOR):latest flightctl-imagebuilder-worker:latest; \
		sudo podman tag flightctl-userinfo-proxy-$(FLAVOR):latest flightctl-userinfo-proxy:latest; \
		sudo podman tag flightctl-telemetry-gateway-$(FLAVOR):latest flightctl-telemetry-gateway:latest; \
	else \
		echo "Flavor-specific images not found, using legacy container naming (old system)..."; \
		podman save flightctl-api:latest | sudo podman load; \
		podman save flightctl-db-setup:latest | sudo podman load; \
		podman save flightctl-worker:latest | sudo podman load; \
		podman save flightctl-periodic:latest | sudo podman load; \
		podman save flightctl-alert-exporter:latest | sudo podman load; \
		podman save flightctl-cli-artifacts:latest | sudo podman load; \
		podman save flightctl-alertmanager-proxy:latest | sudo podman load; \
		podman save flightctl-pam-issuer:latest | sudo podman load; \
		podman save flightctl-imagebuilder-api:latest | sudo podman load; \
		podman save flightctl-imagebuilder-worker:latest | sudo podman load; \
		podman save flightctl-userinfo-proxy:latest | sudo podman load; \
		podman save flightctl-telemetry-gateway:latest | sudo podman load; \
		echo "Legacy images already have correct names for quadlets, no tagging needed."; \
	fi
endif
	$(MAKE) build-standalone
	sudo FLAVOR=$(FLAVOR) -E deploy/scripts/deploy_quadlets.sh

kill-db:
	sudo systemctl stop flightctl-db.service

kill-kv:
	sudo systemctl stop flightctl-kv.service

kill-alertmanager:
	sudo systemctl stop flightctl-alertmanager.service

kill-alertmanager-proxy:
	sudo systemctl stop flightctl-alertmanager-proxy.service

show-podman-secret:
	sudo podman secret inspect $(SECRET_NAME) --showsecret | jq '.[] | .SecretData'

# Can set the image tag to a specific version by using the PACKIT_CURRENT_VERSION variable
# which builds the rpm with the specified version.
#
# Example cmd:
# PACKIT_CURRENT_VERSION=latest make services-container
services-container: PACKIT_CURRENT_VERSION ?= latest
services-container: rpm
	@test -f bin/rpm/flightctl-services-*.rpm || (echo "No RPM file found - RPM build failed" && exit 1)
	sudo podman build -t flightctl-services:latest -f test/scripts/services-images/Containerfile.services .

run-services-container:
	@if ! sudo podman image exists localhost/flightctl-services:latest; then \
		echo "Container image not found, building flightctl-services container..."; \
		$(MAKE) services-container; \
	else \
		echo "Using existing flightctl-services container image"; \
	fi
	sudo podman run -d --privileged --replace \
	--name flightctl-services \
	-p 443:443 \
	-p 3443:3443 \
	-p 7443:7443 \
	-p 8090:8090 \
	-p 8443:8443 \
	-p 9093:9093 \
	localhost/flightctl-services:latest

clean-services-container:
	sudo podman stop flightctl-services || true
	sudo podman rm flightctl-services || true
	sudo podman rmi localhost/flightctl-services:latest || true

PHONY: deploy-db deploy cluster services-container run-services-container clean-services-container

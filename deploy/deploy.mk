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

deploy: cluster build-containers build-cli deploy-helm prepare-agent-config

redeploy-api: flightctl-api-container
	test/scripts/redeploy.sh api

redeploy-worker: flightctl-worker-container
	test/scripts/redeploy.sh worker

redeploy-periodic: flightctl-periodic-container
	test/scripts/redeploy.sh periodic

redeploy-alert-exporter: flightctl-alert-exporter-container
	test/scripts/redeploy.sh alert-exporter

redeploy-alertmanager-proxy: flightctl-alertmanager-proxy-container
	test/scripts/redeploy.sh alertmanager-proxy

deploy-helm: flightctl-api-container flightctl-db-setup-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-alertmanager-proxy-container flightctl-multiarch-cli-container
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

deploy-quadlets: build-containers
	@echo "Copying containers from user to root context for systemd services..."
	podman save flightctl-api:latest | sudo podman load
	podman save flightctl-db-setup:latest | sudo podman load
	podman save flightctl-worker:latest | sudo podman load
	podman save flightctl-periodic:latest | sudo podman load
	podman save flightctl-alert-exporter:latest | sudo podman load
	podman save flightctl-cli-artifacts:latest | sudo podman load
	podman save flightctl-alertmanager-proxy:latest | sudo podman load
	sudo -E deploy/scripts/deploy_quadlets.sh

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

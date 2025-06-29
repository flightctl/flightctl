ifeq ($(DB_SIZE),)
	DB_SIZE := e2e
endif

ifeq ($(STATUS_UPDATE_INTERVAL),)
	STATUS_UPDATE_INTERVAL := 0m2s
endif

ifeq ($(SPEC_FETCH_INTERVAL),)
	SPEC_FETCH_INTERVAL := 0m2s
endif

cluster: bin/e2e-certs/ca.pem
	test/scripts/install_kind.sh
	kind get clusters | grep kind || test/scripts/create_cluster.sh

clean-cluster:
	kind delete cluster

deploy: cluster build deploy-helm deploy-e2e-extras prepare-agent-config

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

deploy-helm: git-server-container flightctl-api-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-alertmanager-proxy-container flightctl-multiarch-cli-container
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

deploy-quadlets:
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

.PHONY: deploy-db deploy cluster

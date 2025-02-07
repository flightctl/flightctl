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

deploy-helm: git-server-container flightctl-api-container flightctl-worker-container flightctl-periodic-container
	kubectl config set-context kind-kind
	test/scripts/install_helm.sh
	test/scripts/deploy_with_helm.sh --db-size $(DB_SIZE)

prepare-agent-config:
	test/scripts/agent-images/prepare_agent_config.sh --status-update-interval $(STATUS_UPDATE_INTERVAL) --spec-fetch-interval $(SPEC_FETCH_INTERVAL)

deploy-db-helm: cluster
	test/scripts/deploy_with_helm.sh --only-db

deploy-db:
	deploy/scripts/deploy_quadlet_service.sh db

deploy-kv:
	deploy/scripts/deploy_quadlet_service.sh kv

deploy-quadlets:
	deploy/scripts/deploy_quadlets.sh

kill-db:
	systemctl --user stop flightctl-db-standalone.service

kill-kv:
	systemctl --user stop flightctl-kv-standalone.service

.PHONY: deploy-db deploy cluster

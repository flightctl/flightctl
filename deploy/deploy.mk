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
	sudo systemctl stop flightctl-db-standalone.service || true
	sudo podman volume rm flightctl-db || true
	sudo mkdir -p /etc/containers/systemd/flightctl-db
	sudo cp -r deploy/podman/flightctl-db/* /etc/containers/systemd/flightctl-db
	sudo podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec flightctl-db
	sudo systemctl daemon-reload
	sudo systemctl start flightctl-db-standalone.service
	test/scripts/wait_for_postgres.sh podman
	sudo podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'

deploy-mq:
	sudo systemctl stop flightctl-rabbitmq-standalone.service || true
	sudo mkdir -p /etc/containers/systemd/flightctl-rabbitmq
	sudo cp -r deploy/podman/flightctl-rabbitmq/* /etc/containers/systemd/flightctl-rabbitmq
	sudo systemctl daemon-reload
	sudo systemctl start flightctl-rabbitmq-standalone.service

deploy-kv:
	sudo systemctl stop flightctl-kv-standalone.service || true
	sudo mkdir -p /etc/containers/systemd/flightctl-kv
	sudo cp -r deploy/podman/flightctl-kv/* /etc/containers/systemd/flightctl-kv
	sudo systemctl daemon-reload
	sudo systemctl start flightctl-kv-standalone.service

deploy-quadlets:
	@bash -c 'source ./test/scripts/functions && \
	export PRIMARY_IP=$$(get_ext_ip) && \
	echo "Primary IP: $$PRIMARY_IP" && \
	envsubst "\$$PRIMARY_IP" < deploy/podman/flightctl-api/flightctl-api-config/config.yaml.template > deploy/podman/flightctl-api/flightctl-api-config/config.yaml'
	@sudo cp -r deploy/podman/* /etc/containers/systemd/
	@sudo systemctl daemon-reload
	@sudo systemctl start flightctl.slice
	@echo "Deployment started. Waiting for database..."
	test/scripts/wait_for_postgres.sh podman
	sudo podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
	@echo "Checking if all services are running..."
	@timeout 300s bash -c 'until sudo podman ps --quiet --filter "name=flightctl-api" --filter "name=flightctl-worker" --filter "name=flightctl-periodic" --filter "name=flightctl-db" --filter "name=flightctl-rabbitmq" --filter "name=flightctl-kv" --filter "name=flightctl-ui" | wc -l | grep -q 7; do echo "Waiting for all services to be running..."; sleep 5; done'
	@echo "Deployment completed. Please, login to FlightCtl with the following command:"
	@echo "flightctl login --insecure-skip-tls-verify $(shell cat ./deploy/podman/flightctl-api/flightctl-api-config/config.yaml | grep baseUrl | awk '{print $$2}')"
	@echo "The FlightCtl console is in the following URL: $(shell cat ./deploy/podman/flightctl-api/flightctl-api-config/config.yaml | grep baseUIUrl | awk '{print $$2}')"

kill-db:
	sudo systemctl stop flightctl-db-standalone.service

kill-mq:
	sudo systemctl stop flightctl-rabbitmq-standalone.service

kill-kv:
	sudo systemctl stop flightctl-kv-standalone.service

.PHONY: deploy-db deploy cluster

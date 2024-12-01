ifeq ($(DB_VERSION),)
	DB_VERSION := e2e
endif

cluster: bin/e2e-certs/ca.pem
	test/scripts/install_kind.sh
	kind get clusters | grep kind || test/scripts/create_cluster.sh

clean-cluster:
	kind delete cluster

deploy: cluster build deploy-helm deploy-e2e-extras prepare-agent-config

deploy-helm: git-server-container flightctl-api-container flightctl-worker-container flightctl-periodic-container
	kubectl config set-context kind-kind
	test/scripts/install_helm.sh
	test/scripts/deploy_with_helm.sh --db-version $(DB_VERSION)

prepare-agent-config:
	test/scripts/agent-images/prepare_agent_config.sh

deploy-db-helm: cluster
	test/scripts/deploy_with_helm.sh --only-db

deploy-db:
	podman rm -f flightctl-db || true
	podman volume rm podman_flightctl-db || true
	podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec podman_flightctl-db
	cd deploy/podman && podman-compose up -d flightctl-db
	test/scripts/wait_for_postgres.sh podman
	podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
	podman exec -it flightctl-db createdb admin || true

deploy-mq:
	podman rm -f flightctl-mq || true
	cd deploy/podman && podman-compose up -d flightctl-mq

deploy-kv:
	podman rm -f flightctl-kv || true
	cd deploy/podman && podman-compose up -d flightctl-kv

deploy-quadlets:
	@bash -c 'source ./test/scripts/functions && \
	export PRIMARY_IP=$$(get_ext_ip) && \
	echo "Primary IP: $$PRIMARY_IP" && \
	envsubst "\$$PRIMARY_IP" < deploy/quadlets/flightctl-api/flightctl-api-config/config.yaml.template > deploy/quadlets/flightctl-api/flightctl-api-config/config.yaml'
	@sudo cp -r deploy/quadlets/* /etc/containers/systemd/
	@sudo systemctl daemon-reload
	@sudo systemctl start flightctl.slice
	@echo "Deployment started. Checking if services are running..."
	@timeout 300s bash -c 'until sudo podman ps --quiet --filter "name=flightctl-api" --filter "name=flightctl-worker" --filter "name=flightctl-periodic" --filter "name=flightctl-db" --filter "name=flightctl-rabbitmq" --filter "name=flightctl-kv" --filter "name=flightctl-ui" | wc -l | grep -q 7; do echo "Waiting for all services to be running..."; sleep 5; done'
	@echo "Deployment completed. Please, login to FlightCtl with the following command:"
	@echo "flightctl login --insecure-skip-tls-verify $(shell cat ./deploy/quadlets/flightctl-api/flightctl-api-config/config.yaml | grep baseUrl | awk '{print $$2}')"
	@echo "The FlightCtl console is in the following URL: $(shell cat ./deploy/quadlets/flightctl-api/flightctl-api-config/config.yaml | grep baseUIUrl | awk '{print $$2}')"

kill-db:
	cd deploy/podman && podman-compose down flightctl-db

kill-mq:
	cd deploy/podman && podman-compose down flightctl-mq

kill-kv:
	cd deploy/podman && podman-compose down flightctl-kv

.PHONY: deploy-db deploy cluster run-db-container kill-db-container

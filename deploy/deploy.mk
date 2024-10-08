
cluster: bin/e2e-certs/ca.pem
	test/scripts/install_kind.sh
	kind get clusters | grep kind || test/scripts/create_cluster.sh

clean-cluster:
	kind delete cluster

deploy: cluster build deploy-helm prepare-agent-config

deploy-helm: git-server-container flightctl-api-container flightctl-worker-container flightctl-periodic-container
	kubectl config set-context kind-kind
	test/scripts/install_helm.sh
	test/scripts/deploy_with_helm.sh

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

kill-db:
	cd deploy/podman && podman-compose down flightctl-db

kill-mq:
	cd deploy/podman && podman-compose down flightctl-mq

.PHONY: deploy-db deploy cluster run-db-container kill-db-container
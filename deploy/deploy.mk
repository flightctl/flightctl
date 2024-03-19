cluster:
	./hack/install_kind.sh
	kind get clusters | grep kind || ./hack/create_cluster.sh

clean-cluster:
	kind delete cluster

deploy: flightctl-server-container cluster deploy-helm prepare-agent-config

deploy-helm:
	kubectl config set-context kind-kind
	hack/install_helm.sh
	hack/deploy_with_helm.sh

prepare-agent-config:
	hack/prepare_agent_config.sh

deploy-db-helm: cluster
	hack/deploy_with_helm.sh --only-db

deploy-db:
	podman rm -f flightctl-db || true
	podman volume rm podman_flightctl-db || true
	podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec podman_flightctl-db
	cd deploy/podman && podman-compose up -d flightctl-db
	hack/wait_for_postgres.sh podman
	podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
	podman exec -it flightctl-db createdb admin || true

kill-db:
	cd deploy/podman && podman-compose down flightctl-db

.PHONY: deploy-db deploy cluster run-db-container kill-db-container
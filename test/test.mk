REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
GO_TESTING_FLAGS= -count=1 -race $(GO_BUILD_FLAGS)

GO_UNITTEST_DIRS 		= ./internal/... ./api/...
GO_INTEGRATIONTEST_DIRS = ./test/integration/...
GO_E2E_DIRS 			= ./test/e2e/...

GO_UNITTEST_FLAGS 		 = $(GO_TESTING_FLAGS) $(GO_UNITTEST_DIRS)        -coverprofile=$(REPORTS)/unit-coverage.out
GO_INTEGRATIONTEST_FLAGS = $(GO_TESTING_FLAGS) $(GO_INTEGRATIONTEST_DIRS) -coverprofile=$(REPORTS)/integration-coverage.out

# Common environment flags for test tracing enforcement
ENV_TRACE_FLAGS = TRACE_TESTS=false GORM_TRACE_ENFORCE_FATAL=true GORM_TRACE_INCLUDE_QUERY_VARIABLES=true

ifeq ($(VERBOSE), true)
	GO_TEST_FORMAT=standard-verbose
	GO_UNITTEST_FLAGS += -v
	GO_INTEGRATIONTEST_FLAGS += -v
endif

GO_TEST_FLAGS := 			 --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_unit_test.xml $(GOTEST_PUBLISH_FLAGS)
GO_TEST_INTEGRATION_FLAGS := --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_integration_test.xml $(GOTEST_PUBLISH_FLAGS)
KUBECONFIG_PATH = '/home/kni/clusterconfigs/auth/kubeconfig'

_integration_test: $(REPORTS)
	go run -modfile=tools/go.mod gotest.tools/gotestsum $(GO_TEST_E2E_FLAGS) -- $(GO_INTEGRATIONTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _collect_junit && /bin/false)
	$(MAKE) _collect_junit

_e2e_test: $(REPORTS)
	sudo chown $(shell whoami):$(shell whoami) -R bin/output
	test/scripts/run_e2e_tests.sh "$(REPORTS)" $(GO_E2E_DIRS)

_unit_test: $(REPORTS)
	go run -modfile=tools/go.mod gotest.tools/gotestsum $(GO_TEST_FLAGS) -- $(GO_UNITTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _collect_junit && /bin/false)
	$(MAKE) _collect_junit

_collect_junit: $(REPORTS)
	@for name in `find '$(ROOT_DIR)' -name 'junit*.xml' -type f -not -path '$(REPORTS)/*'`; do \
		mv -f $$name $(REPORTS)/junit_unit_$$(basename $$(dirname $$name)).xml; \
	done

unit-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _unit_test TEST="$(or $(TEST),$(shell go list ./pkg/... ./internal/... ./cmd/...))"

run-integration-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _integration_test TEST="$(or $(TEST),$(shell go list ./test/integration/...))"

integration-test: export FLIGHTCTL_KV_PASSWORD=adminpass
integration-test: export FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD=adminpass
integration-test: export FLIGHTCTL_POSTGRESQL_USER_PASSWORD=adminpass
integration-test: export DB_APP_PASSWORD=adminpass
integration-test: export DB_MIGRATION_PASSWORD=adminpass
integration-test:
	@set -e; \
	$(MAKE) deploy-db deploy-kv deploy-alertmanager; \
	echo "Granting migration privileges to flightctl_app user for integration tests..."; \
	timeout --foreground 60s bash -c ' \
		while ! sudo podman exec flightctl-db psql -U admin -d flightctl -c "SELECT 1" >/dev/null 2>&1; do \
			echo "Waiting for database to be ready..."; \
			sleep 2; \
		done \
	'; \
	sudo podman exec flightctl-db psql -U admin -d flightctl -c "ALTER USER flightctl_app CREATEDB; GRANT CREATE ON SCHEMA public TO flightctl_app; GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO flightctl_app; GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO flightctl_app; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO flightctl_app; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO flightctl_app;" || true; \
	trap '$(MAKE) -k kill-alertmanager kill-kv kill-db' EXIT; \
	$(MAKE) run-integration-test

deploy-e2e-extras: bin/.ssh/id_rsa.pub bin/e2e-certs/ca.pem
	test/scripts/deploy_e2e_extras_with_helm.sh

deploy-e2e-ocp-test-vm:
	sudo test/scripts/create_vm_libvirt.sh ${KUBECONFIG_PATH}

prepare-e2e-test: deploy-e2e-extras bin/output/qcow2/disk.qcow2
	./test/scripts/prepare_cli.sh

in-cluster-e2e-test: prepare-e2e-test
	$(MAKE) _e2e_test

e2e-test: deploy bin/output/qcow2/disk.qcow2
	$(MAKE) _e2e_test

run-e2e-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _e2e_test


view-coverage: $(REPORTS)/unit-coverage.out $(REPORTS)/unit-coverage.out
	# TODO: merge unit and integration coverage reports
	go tool cover -html=$(REPORTS)/unit-coverage.out -html=$(REPORTS)/integration-coverage.out

test: unit-test integration-test e2e-test

run-test: unit-test run-intesgration-test

bin/e2e-certs/ca.pem bin/.ssh/id_rsa.pub:
	test/scripts/create_e2e_certs.sh

git-server-container: bin/.ssh/id_rsa.pub
	test/scripts/prepare_git_server.sh

.PHONY: test run-test git-server-container

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/unit-coverage.out:
	$(MAKE) unit-test || true


$(REPORTS)/integration-coverage.out:
	$(MAKE) integration-test || true

.PHONY: unit-test prepare-integration-test integration-test run-integration-test view-coverage prepare-e2e-test deploy-e2e-ocp-test-vm

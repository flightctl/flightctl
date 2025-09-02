REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
GO_TESTING_FLAGS= -count=1 -race $(GO_BUILD_FLAGS)

GO_UNITTEST_DIRS 		= ./internal/... ./api/...
GO_INTEGRATIONTEST_DIRS ?= ./test/integration/...
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
	go run -modfile=tools/go.mod gotest.tools/gotestsum $(GO_TEST_INTEGRATION_FLAGS) -- $(GO_INTEGRATIONTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _collect_junit && /bin/false)
	$(MAKE) _collect_junit

_e2e_test: $(REPORTS)
	sudo chown $(shell whoami):$(shell whoami) -R bin/output
	sudo chmod a+x test/scripts/setup_e2e_environment.sh test/scripts/e2e_cleanup.sh test/scripts/e2e_startup.sh
	test/scripts/setup_e2e_environment.sh
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
integration-test: export FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD=adminpass
integration-test: export FLIGHTCTL_TEST_DB_STRATEGY?=local

integration-test:
	@bash -euo pipefail -c '\
	  trap "set +e; $(MAKE) -k kill-alertmanager kill-kv kill-db || true" EXIT; \
	  echo "Using $(FLIGHTCTL_TEST_DB_STRATEGY) database strategy..."; \
	  $(MAKE) deploy-db deploy-kv deploy-alertmanager; \
	  $(MAKE) _wait_for_db; \
	  sudo podman exec flightctl-db psql -U admin -d postgres -c "ALTER USER flightctl_app CREATEDB;"; \
	  if [[ "$(FLIGHTCTL_TEST_DB_STRATEGY)" == "template" ]]; then \
	    $(MAKE) _run_template_migration; \
	  else \
	    echo "Local strategy: skipping migration image — tests will run local migrations..."; \
	  fi; \
	  echo "##################################################"; \
	  echo "Running integration tests: $(FLIGHTCTL_TEST_DB_STRATEGY)"; \
	  echo "##################################################"; \
	  $(MAKE) run-integration-test \
	'

_wait_for_db:
	@echo "Waiting for database to be ready..."
	@timeout --foreground 60s bash -euo pipefail -c '\
	  while ! sudo podman exec flightctl-db psql -U admin -d postgres -c "SELECT 1" >/dev/null 2>&1; do \
	    echo "  ...still waiting"; \
	    sleep 2; \
	  done' || { echo "ERROR: Database did not become ready within 60s"; exit 1; }

_run_template_migration:
	@MIGRATION_IMAGE="$(MIGRATION_IMAGE)" bash -euo pipefail -c '\
	  echo "Template strategy: resolving migration image..."; \
	  if [ -n "$$MIGRATION_IMAGE" ]; then \
	    echo "##################################################"; \
	    echo "Using provided migration image: $$MIGRATION_IMAGE"; \
	    echo "##################################################"; \
	    if ! sudo podman image exists "$$MIGRATION_IMAGE"; then \
	      echo "Image not found locally; attempting to pull..."; \
	      if ! sudo podman pull "$$MIGRATION_IMAGE"; then \
	        echo "Error: failed to pull $$MIGRATION_IMAGE" >&2; exit 1; \
	      fi; \
	    fi; \
	    img="$$MIGRATION_IMAGE"; \
	  else \
	    echo "##################################################"; \
	    echo "No MIGRATION_IMAGE provided; building a fresh one ..."; \
	    echo "##################################################"; \
	    $(MAKE) --no-print-directory -B flightctl-db-setup-container; \
	    img="flightctl-db-setup:latest"; \
	    if ! sudo podman image exists "$$img"; then \
	      echo "Error: build did not produce $$img" >&2; exit 1; \
	    fi; \
	  fi; \
	  echo "##################################################"; \
	  echo "Running database migration & template creation using: $$img"; \
	  echo "##################################################"; \
	  sudo env MIGRATION_IMAGE="$$img" CREATE_TEMPLATE=true \
	    test/scripts/run_migration.sh \
	'

deploy-e2e-extras: bin/.ssh/id_rsa.pub bin/e2e-certs/ca.pem
	test/scripts/deploy_e2e_extras_with_helm.sh

deploy-e2e-ocp-test-vm:
	sudo --preserve-env=VM_DISK_SIZE_INC test/scripts/create_vm_libvirt.sh ${KUBECONFIG_PATH}

prepare-e2e-test: deploy-e2e-extras bin/output/qcow2/disk.qcow2 build-e2e-containers
	./test/scripts/prepare_cli.sh

# Build E2E containers with Docker caching
build-e2e-containers: git-server-container e2e-agent-images
	@echo "Building E2E containers with Docker caching..."

# Ensure git-server container is built with proper caching
git-server-container: bin/e2e-certs/ca.pem
	@echo "Building git-server container with Docker caching..."
	test/scripts/prepare_git_server.sh
	@if test/scripts/functions in_kind; then \
		echo "Loading git-server into kind cluster..."; \
		source test/scripts/functions && kind_load_image localhost/git-server:latest; \
	fi

# Build E2E agent images with proper caching
e2e-agent-images: bin/.e2e-agent-images
	@echo "E2E agent images already built and up to date"

in-cluster-e2e-test: prepare-e2e-test
	$(MAKE) _e2e_test

e2e-test: deploy bin/output/qcow2/disk.qcow2
	$(MAKE) _e2e_test

# Run e2e tests with optional parallel execution
# Set GINKGO_PROCS to control number of parallel processes (defaults to number of CPU cores)
# Set GINKGO_OUTPUT_INTERCEPTOR_MODE to control parallel output (defaults to "dup" for full output)
# Example: make run-e2e-test GO_E2E_DIRS=test/e2e/agent GINKGO_PROCS=4
# Example: make run-e2e-test GO_E2E_DIRS=test/e2e/agent GINKGO_OUTPUT_INTERCEPTOR_MODE=swap
run-e2e-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _e2e_test

view-coverage: $(REPORTS)/unit-coverage.out $(REPORTS)/unit-coverage.out
	# TODO: merge unit and integration coverage reports
	go tool cover -html=$(REPORTS)/unit-coverage.out -html=$(REPORTS)/integration-coverage.out

test: unit-test integration-test e2e-test

run-test: unit-test run-integration-test

# Create E2E certificates and SSH keys
bin/e2e-certs/ca.pem bin/.ssh/id_rsa.pub:
	test/scripts/create_e2e_certs.sh



.PHONY: test run-test git-server-container

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/unit-coverage.out:
	$(MAKE) unit-test || true


$(REPORTS)/integration-coverage.out:
	$(MAKE) integration-test || true

.PHONY: unit-test prepare-integration-test integration-test run-integration-test view-coverage prepare-e2e-test deploy-e2e-ocp-test-vm _wait_for_db _run_template_migration _ensure_db_setup_image

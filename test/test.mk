REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
GO_TESTING_FLAGS= -count=1 -race $(GO_BUILD_FLAGS)

GO_UNITTEST_DIRS 		= ./internal/... ./api/... ./pkg/...
GO_INTEGRATIONTEST_DIRS ?= ./test/integration/...
GO_E2E_DIRS 			= ./test/e2e/...

GO_UNITTEST_FLAGS 		 = $(GO_TESTING_FLAGS) $(GO_UNITTEST_DIRS)        -coverprofile=$(REPORTS)/unit-coverage.out
GO_INTEGRATIONTEST_FLAGS = $(GO_TESTING_FLAGS) $(if $(TEST_DIR),$(TEST_DIR),$(GO_INTEGRATIONTEST_DIRS)) $(if $(TESTS),-run $(TESTS)) -coverprofile=$(REPORTS)/integration-coverage.out

# Common environment flags for test tracing enforcement
ENV_TRACE_FLAGS = TRACE_TESTS=false GORM_TRACE_ENFORCE_FATAL=true GORM_TRACE_INCLUDE_QUERY_VARIABLES=true

ifeq ($(VERBOSE), true)
	GO_TEST_FORMAT=standard-verbose
	GO_UNITTEST_FLAGS += -v
	GO_INTEGRATIONTEST_FLAGS += -v
	ENV_TRACE_FLAGS += LOG_LEVEL=debug
endif

GO_TEST_FLAGS := 			 --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_unit_test.xml $(GOTEST_PUBLISH_FLAGS)
GO_TEST_INTEGRATION_FLAGS := --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_integration_test.xml $(GOTEST_PUBLISH_FLAGS)
KUBECONFIG_PATH = '/home/kni/clusterconfigs/auth/kubeconfig'
TEMP_SWTPM_CERT_DIR := bin/tmp/swtpm-certs

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
	$(ENV_TRACE_FLAGS) $(MAKE) _integration_test TEST="$(or $(TEST),$(shell go list $(if $(TEST_DIR),$(TEST_DIR),./test/integration/...)))"


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
	  sudo -E env MIGRATION_IMAGE="$$img" CREATE_TEMPLATE=true \
    test/scripts/run_migration.sh \
	'

deploy-e2e-extras: bin/.ssh/id_rsa.pub bin/e2e-certs/ca.pem git-server-container
	test/scripts/deploy_e2e_extras_with_helm.sh

deploy-e2e-ocp-test-vm:
	sudo --preserve-env=VM_DISK_SIZE_INC test/scripts/create_vm_libvirt.sh ${KUBECONFIG_PATH}

deploy-quadlets-vm:
	sudo --preserve-env=VM_DISK_SIZE_INC --preserve-env=USER --preserve-env=REDHAT_USER --preserve-env=REDHAT_PASSWORD --preserve-env=GIT_VERSION --preserve-env=BREW_BUILD_URL test/scripts/deploy_quadlets_rhel.sh

clean-quadlets-vm:
	@echo "Cleaning up quadlets-vm..."
	@sudo virsh destroy quadlets-vm 2>/dev/null || true
	@sudo virsh undefine quadlets-vm 2>/dev/null || true
	@sudo rm -f /var/lib/libvirt/images/quadlets-vm.qcow2 2>/dev/null || true
	@sudo rm -f /var/lib/libvirt/images/quadlets-vm_src.qcow2 2>/dev/null || true
	@echo "quadlets-vm cleanup completed"

bin/.e2e-agent-injected: bin/output/qcow2/disk.qcow2 bin/.e2e-agent-certs
	QCOW=bin/output/qcow2/disk.qcow2 AGENT_DIR=bin/agent/etc/flightctl test/scripts/inject_agent_files_into_qcow.sh
	touch bin/.e2e-agent-injected

prepare-e2e-qcow-config: bin/.e2e-agent-injected

prepare-e2e-test: RPM_MOCK_ROOT=centos-stream+epel-next-9-x86_64
prepare-e2e-test: deploy-e2e-extras build-e2e-containers push-e2e-agent-images prepare-e2e-qcow-config
	./test/scripts/prepare_cli.sh

# Build E2E containers with Docker caching
build-e2e-containers: git-server-container e2e-agent-images
	@echo "Building E2E containers with Docker caching..."

# Ensure git-server container is built with proper caching
git-server-container: bin/e2e-certs/ca.pem
	@echo "Building git-server container with Docker caching..."
	test/scripts/prepare_git_server.sh
	@bash -c 'source test/scripts/functions && in_kind && echo "Loading git-server into kind cluster..." && kind_load_image localhost/git-server:latest' || true

# Build E2E agent images with proper caching (offline build – no cert generation)
# Sentinel file includes AGENT_OS_ID to ensure rebuilds when OS changes
E2E_AGENT_IMAGES_SENTINEL := $(ROOT_DIR)/bin/.e2e-agent-images-$(AGENT_OS_ID)

e2e-agent-images: $(E2E_AGENT_IMAGES_SENTINEL)
	@echo "E2E agent images already built and up to date"

in-cluster-e2e-test: prepare-e2e-test
	$(MAKE) _e2e_test

e2e-test: RPM_MOCK_ROOT=centos-stream+epel-next-9-x86_64
e2e-test: deploy prepare-e2e-qcow-config
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

prepare-swtpm-certs:
	@mkdir -p $(TEMP_SWTPM_CERT_DIR)
	# swtpm-localca may require root access so setup a directory in which the current user has access to
	@sudo sh -c 'if ls /var/lib/swtpm-localca/*cert.pem >/dev/null 2>&1; then cp /var/lib/swtpm-localca/*cert.pem $(abspath $(TEMP_SWTPM_CERT_DIR))/; else echo "No swtpm certificates found - they may not be generated yet"; exit 1; fi'
	@sudo chown -R $(shell id -u):$(shell id -g) $(TEMP_SWTPM_CERT_DIR)
	test/scripts/add-certs-to-deployment.sh $(TEMP_SWTPM_CERT_DIR)

clean-swtpm-certs:
	rm -rf $(TEMP_SWTPM_CERT_DIR)

.PHONY: test run-test git-server-container e2e-agent-images push-e2e-agent-images

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/unit-coverage.out:
	$(MAKE) unit-test || true


$(REPORTS)/integration-coverage.out:
	$(MAKE) integration-test || true

.PHONY: unit-test prepare-integration-test integration-test run-integration-test view-coverage prepare-e2e-test deploy-e2e-ocp-test-vm _wait_for_db _run_template_migration _ensure_db_setup_image prepare-swtpm-certs clean-swtpm-certs

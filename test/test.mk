REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
# RACE=0 disables the race detector (faster for local iteration; CI always uses RACE=1).
RACE ?= 1
# COVERAGE=0 disables the unit-test coverage profile (faster for local iteration; CI always uses COVERAGE=1).
COVERAGE ?= 1

GO_TESTING_FLAGS= $(GO_BUILD_FLAGS)
ifeq ($(RACE), 1)
	GO_TESTING_FLAGS += -race
endif

GO_UNITTEST_DIRS 		= ./internal/... ./api/... ./pkg/...
GO_INTEGRATIONTEST_DIRS ?= ./test/integration/...
GO_E2E_DIRS 			= ./test/e2e/...

# Integration-only: run integration tests this many times. Uses ginkgo --repeat (N-1 for N total runs).
# Stops on first failure.
INTEGRATION_TEST_COUNT ?= 1
# Optional Ginkgo focus regex for suites under test/integration that use Ginkgo (e.g. imagebuilder_worker, agent).
INTEGRATION_GINKGO_FOCUS ?=
# Number of parallel test suite processes. Each suite gets its own ephemeral Redis container.
# Set to 1 to disable parallel execution. Default: 4.
INTEGRATION_PROCS ?= 4

GO_UNITTEST_FLAGS 		 = $(GO_TESTING_FLAGS) $(GO_UNITTEST_DIRS)
ifeq ($(COVERAGE), 1)
	GO_UNITTEST_FLAGS += -coverprofile=$(REPORTS)/unit-coverage.out
endif

# Common environment flags for test tracing enforcement
ENV_TRACE_FLAGS = TRACE_TESTS=false GORM_TRACE_ENFORCE_FATAL=true GORM_TRACE_INCLUDE_QUERY_VARIABLES=true

ifeq ($(VERBOSE), true)
	GO_TEST_FORMAT=standard-verbose
	GO_UNITTEST_FLAGS += -v
	ENV_TRACE_FLAGS += LOG_LEVEL=debug
endif

GO_TEST_FLAGS := 			 --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_unit_test.xml $(GOTEST_PUBLISH_FLAGS)
KUBECONFIG_PATH = '/home/kni/clusterconfigs/auth/kubeconfig'
TEMP_SWTPM_CERT_DIR := bin/tmp/swtpm-certs

_integration_test: $(REPORTS)
	@go install github.com/onsi/ginkgo/v2/ginkgo
	@GOBIN=$$(go env GOBIN); \
	if [ -z "$$GOBIN" ]; then GOBIN=$$(go env GOPATH)/bin; fi; \
	count="$(INTEGRATION_TEST_COUNT)"; \
	repeat_flag=""; \
	if [ "$${count:-1}" -gt 1 ] 2>/dev/null; then repeat_flag="--repeat=$$((count - 1))"; fi; \
	$$GOBIN/ginkgo run \
		--race \
		--procs=$(INTEGRATION_PROCS) \
		--timeout=$(TIMEOUT) \
		--output-dir=$(REPORTS) \
		--junit-report=junit_integration_test.xml \
		--json-report=integration_timing.json \
		--keep-going \
		$(if $(VERBOSE),--vv) \
		$(if $(strip $(INTEGRATION_GINKGO_FOCUS)),--focus="$(INTEGRATION_GINKGO_FOCUS)") \
		$(if $(TESTS),--focus="$(TESTS)") \
		$${repeat_flag} \
		$(if $(TEST_DIR),$(TEST_DIR),$(GO_INTEGRATIONTEST_DIRS))

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
	$(ENV_TRACE_FLAGS) $(MAKE) _unit_test TEST="$(or $(TEST),$(shell go list ./pkg/... ./internal/... ./cmd/... ./deploy/helm/...))"

run-integration-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _integration_test TEST="$(or $(TEST),$(shell go list $(if $(TEST_DIR),$(TEST_DIR),./test/integration/...)))"

BIN_PREFLIGHT := $(ROOT_DIR)/bin/preflight
PREFLIGHT_SRC := $(wildcard $(ROOT_DIR)/test/integration/preflight/*.go)

$(BIN_PREFLIGHT): $(PREFLIGHT_SRC)
	@mkdir -p "$(ROOT_DIR)/bin"
	cd "$(ROOT_DIR)" && go build -o $@ ./test/integration/preflight

.PHONY: build-integration-preflight
build-integration-preflight: $(BIN_PREFLIGHT)

# Start integration testcontainers (Postgres, Alertmanager) and run migrations.
# Redis is NOT started here - each test suite creates its own ephemeral Redis container for isolation.
# Migrations are run via 'go run ./cmd/flightctl-db-migrate' to always use current source code.
start-integration-services: $(BIN_PREFLIGHT)
	@cd "$(ROOT_DIR)" && "$(BIN_PREFLIGHT)" start
	@cd "$(ROOT_DIR)" && "$(BIN_PREFLIGHT)" migrate

# Stop integration testcontainers.
stop-integration-services: $(BIN_PREFLIGHT)
	@cd "$(ROOT_DIR)" && "$(BIN_PREFLIGHT)" stop || true

integration-test: export FLIGHTCTL_KV_PASSWORD=adminpass
integration-test: export FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD=adminpass
integration-test: export FLIGHTCTL_POSTGRESQL_USER_PASSWORD=adminpass
integration-test: export FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD=adminpass

# Starts integration containers, runs migrations via flightctl-db-migrate binary (same as production),
# then runs tests. Each test clones from the migrated 'flightctl' database for isolation.
integration-test:
	@bash -euo pipefail -c '\
	  trap "set +e; if [ \"$${KEEP_INTEGRATION_STACK:-}\" != \"1\" ]; then $(MAKE) -C \"$(ROOT_DIR)\" stop-integration-services || true; fi" EXIT; \
	  $(MAKE) -C "$(ROOT_DIR)" start-integration-services; \
	  echo "##################################################"; \
	  echo "Running integration tests"; \
	  echo "##################################################"; \
	  $(MAKE) -C "$(ROOT_DIR)" run-integration-test \
	'

# DEPRECATED: deploy-e2e-extras is no longer used
# E2E infrastructure (registry, git-server, prometheus) is now managed by testcontainers
# in test/e2e/infra/ package. The containers start automatically when tests run.
deploy-e2e-extras:
	@echo "WARNING: deploy-e2e-extras is deprecated. Testcontainers now manage E2E infrastructure."
	@echo "See test/e2e/infra/ for the new implementation."

deploy-e2e-ocp-test-vm: VM_DISK_SIZE_INC := $(or $(VM_DISK_SIZE_INC),50)
deploy-e2e-ocp-test-vm:
	sudo VM_DISK_SIZE_INC=$(VM_DISK_SIZE_INC) IPV6_ONLY=$(IPV6_ONLY) --preserve-env=VM_DISK_SIZE_INC --preserve-env=IPV6_ONLY test/scripts/create_vm_libvirt.sh $(KUBECONFIG_PATH)

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
	QCOW=bin/output/qcow2/disk.qcow2 AGENT_DIR=bin/agent/etc/flightctl IPV6_ONLY=${IPV6_ONLY} QUADLET_HOST=${E2E_SSH_HOST} test/scripts/inject_agent_files_into_qcow.sh
	touch bin/.e2e-agent-injected

prepare-e2e-qcow-config: bin/.e2e-agent-injected

prepare-e2e-test: RPM_MOCK_ROOT=centos-stream+epel-next-9-x86_64
# Note: deploy-e2e-extras and push-e2e-agent-images removed
# Testcontainers now handle registry/git-server/prometheus AND image uploading at test runtime
# SSH keys and certs are still needed for git server authentication
prepare-e2e-test: bin/.ssh/id_rsa.pub bin/e2e-certs/ca.pem build-e2e-containers prepare-e2e-qcow-config
	./test/scripts/prepare_cli.sh

# Build E2E containers with Docker caching
# Note: git-server container is now built at test runtime by testcontainers
build-e2e-containers: e2e-agent-images
	@echo "Building E2E containers with Docker caching..."

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
# Set GINKGO_KEEP_GOING=true to run all suites even when one fails (default: stop after first suite failure).
run-e2e-test:
	$(ENV_TRACE_FLAGS) $(MAKE) _e2e_test

view-coverage: $(REPORTS)/unit-coverage.out
	go tool cover -html=$(REPORTS)/unit-coverage.out

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

clean-e2e-certs:
	rm -rf bin/e2e-certs bin/.ssh

.PHONY: test run-test e2e-agent-images push-e2e-agent-images clean-e2e-certs

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/unit-coverage.out:
	$(MAKE) unit-test || true

start-registry: bin/e2e-certs/ca.pem
	go run ./cmd/aux-service start registry

stop-registry:
	go run ./cmd/aux-service stop registry

start-git-server: bin/e2e-certs/ca.pem
	go run ./cmd/aux-service start git-server

stop-git-server:
	go run ./cmd/aux-service stop git-server

start-prometheus:
	go run ./cmd/aux-service start prometheus

stop-prometheus:
	go run ./cmd/aux-service stop prometheus

start-tracing:
	go run ./cmd/aux-service start tracing

stop-tracing:
	go run ./cmd/aux-service stop tracing

start-keycloak:
	go run ./cmd/aux-service start keycloak

stop-keycloak:
	go run ./cmd/aux-service stop keycloak

start-trustify:
	go run ./cmd/aux-service start trustify

stop-trustify:
	go run ./cmd/aux-service stop trustify

start-aux: bin/e2e-certs/ca.pem
	go run ./cmd/aux-service start all

stop-aux:
	go run ./cmd/aux-service stop all

.PHONY: start-registry stop-registry start-git-server stop-git-server start-prometheus stop-prometheus start-tracing stop-tracing start-keycloak stop-keycloak start-trustify stop-trustify start-aux stop-aux
.PHONY: unit-test prepare-integration-test integration-test run-integration-test build-integration-preflight start-integration-services stop-integration-services view-coverage prepare-e2e-test deploy-e2e-ocp-test-vm prepare-swtpm-certs clean-swtpm-certs

# Schemathesis API testing
SCHEMATHESIS_IMAGE ?= flightctl-schemathesis:latest

.PHONY: schemathesis-image test-api clean-schemathesis

schemathesis-image:
	echo "Building Schemathesis container image..."; \
	podman build -f test/api/Containerfile \
		-t $(SCHEMATHESIS_IMAGE) test/api/; \

test-api: schemathesis-image
	SCHEMATHESIS_IMAGE=$(SCHEMATHESIS_IMAGE) SCHEMATHESIS_SUITES=$(SCHEMATHESIS_SUITES) test/api/run_tests.sh

clean-schemathesis:
	-podman rmi $(SCHEMATHESIS_IMAGE) 2>/dev/null || true
	-rm -rf $(REPORTS)/schemathesis

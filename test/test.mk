REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
GO_TESTING_FLAGS= -count=1 -race $(GO_BUILD_FLAGS)

GO_UNITTEST_DIRS = ./internal/...
GO_INTEGRATIONTEST_DIRS = ./test/integration/...

GO_UNITTEST_FLAGS = 	   $(GO_TESTING_FLAGS) $(GO_UNITTEST_DIRS)        -coverprofile=$(REPORTS)/unit-coverage.out
GO_INTEGRATIONTEST_FLAGS = $(GO_TESTING_FLAGS) $(GO_INTEGRATIONTEST_DIRS) -coverprofile=$(REPORTS)/integration-coverage.out

ifeq ($(VERBOSE), true)
	GO_TEST_FORMAT=standard-verbose
	GO_UNITTEST_FLAGS += -v
	GO_INTEGRATIONTEST_FLAGS += -v
endif

GO_TEST_FLAGS := --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_unit_test.xml $(GOTEST_PUBLISH_FLAGS)
GO_TEST_INTEGRATION_FLAGS := --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_integration_test.xml $(GOTEST_PUBLISH_FLAGS)

_integration_test: $(REPORTS)
	gotestsum $(GO_TEST_INTEGRATION_FLAGS) -- $(GO_INTEGRATIONTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _collect_junit && /bin/false)
	$(MAKE) _collect_junit

_unit_test: $(REPORTS)
	gotestsum $(GO_TEST_FLAGS) -- $(GO_UNITTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _collect_junit && /bin/false)
	$(MAKE) _collect_junit

_collect_junit: $(REPORTS)
	@for name in `find '$(ROOT_DIR)' -name 'junit*.xml' -type f -not -path '$(REPORTS)/*'`; do \
		mv -f $$name $(REPORTS)/junit_unit_$$(basename $$(dirname $$name)).xml; \
	done

unit-test:
	$(MAKE) _unit_test TEST="$(or $(TEST),$(shell go list ./pkg/... ./internal/... ./cmd/...))"

run-integration-test:
	$(MAKE) _integration_test TEST="$(or $(TEST),$(shell go list ./...))"

integration-test: deploy-db run-integration-test kill-db

view-coverage: $(REPORTS)/unit-coverage.out $(REPORTS)/unit-coverage.out
	# TODO: merge unit and integration coverage reports
	go tool cover -html=$(REPORTS)/unit-coverage.out -html=$(REPORTS)/integration-coverage.out

test: unit-test integration-test

run-test: unit-test run-intesgration-test

.PHONY: test run-test

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/unit-coverage.out:
	$(MAKE) unit-test || true


$(REPORTS)/integration-coverage.out:
	$(MAKE) integration-test || true

.PHONY: unit-test integration-test run-integration-test view-coverage
GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))
TIMEOUT ?= 30m

VERBOSE ?= false
REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
GO_UNITTEST_FLAGS = -count=1 -race -coverprofile=$(REPORTS)/coverage.out $(GO_BUILD_FLAGS) ./...
ifeq ($(VERBOSE), true)
	GO_TEST_FORMAT=standard-verbose
	GO_UNITTEST_FLAGS += -v
endif

GO_TEST_FLAGS := --format=$(GO_TEST_FORMAT) --junitfile $(REPORTS)/junit_unit_test.xml $(GOTEST_PUBLISH_FLAGS)

.EXPORT_ALL_VARIABLES:

all: build

help:
	@echo "Targets:"
	@echo "    generate:        regenerate all generated files"
	@echo "    tidy:            tidy go mod"
	@echo "    lint:            run golangci-lint"
	@echo "    build:           run all builds"
	@echo "    unit-test:       run unit tests"
	@echo "    deploy:          deploy flightctl-server and db as pods in kind"
	@echo "    deploy-db:       deploy only the database as a pod in kind"
	@echo "    clean:           clean up all containers and volumes"
	@echo "    cluster:         create a kind cluster and load the flightctl-server image"
	@echo "    clean-cluster:   kill the kind cluster only"
	@echo "    rpm/deb:         generate rpm or debian packages"

generate:
	go generate -v $(shell go list ./...)
	hack/mockgen.sh

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

lint: tools
	$(GOBIN)/golangci-lint run -v

build: bin
	go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/...

# rebuild container only on source changes
bin/.flightctl-server-container: bin Containerfile go.mod go.sum $(shell find ./ -name "*.go" -not -path "./packaging/*")
	podman build -f Containerfile -t flightctl-server:latest
	touch bin/.flightctl-server-container

flightctl-server-container: bin/.flightctl-server-container

bin:
	mkdir -p bin

rpm:
	which packit || (echo "Installing packit" && sudo dnf install -y packit)
	rm $(shell uname -m)/flightctl-*.rpm || true
	rm bin/rpm/* || true
	mkdir -p bin/rpm
	packit build locally
	mv $(shell uname -m)/flightctl-*.rpm bin/rpm


# cross-building for deb pkg
bin/amd64:
	GOARCH=amd64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl ./cmd/flightctl/...
	GOARCH=amd64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl-agent ./cmd/flightctl-agent/...

bin/arm64:
	GOARCH=arm64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl ./cmd/flightctl/...
	GOARCH=arm64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl-agent ./cmd/flightctl-agent/...

bin/riscv64:
	GOARCH=riscv64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl ./cmd/flightctl/...
	GOARCH=riscv64 go build -buildvcs=false $(GO_BUILD_FLAGS) -o $@/flightctl-agent ./cmd/flightctl-agent/...



# made as phony targets to make sure they are always rebuilt
.PHONY: bin/arm64 bin/amd64 bin/riscv64

deb-sources: bin/arm64 bin/amd64 bin/riscv64
	ln -f -s packaging/debian debian
	debuild -us -uc -S

deb: bin/arm64 bin/amd64 bin/riscv64
	ln -f -s packaging/debian debian
	debuild -us -uc -b

clean: clean-agent-vm
	- kind delete cluster
	- podman-compose -f deploy/podman/compose.yaml down
	- rm -r ~/.flightctl
	- rm -f -r bin
	- rm -f -r $(shell uname -m)
	- rm -f -r obj-*-linux-gnu
	- rm -f -r debian

_unit_test: $(REPORTS)
	gotestsum $(GO_TEST_FLAGS) -- $(GO_UNITTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _post_unit_test && /bin/false)
	$(MAKE) _post_unit_test

_post_unit_test: $(REPORTS)
	@for name in `find '$(ROOT_DIR)' -name 'junit*.xml' -type f -not -path '$(REPORTS)/*'`; do \
		mv -f $$name $(REPORTS)/junit_unit_$$(basename $$(dirname $$name)).xml; \
	done

run-unit-test:
	SKIP_UT_DB=1 $(MAKE) _unit_test TEST="$(or $(TEST),$(shell go list ./...))"

unit-test: deploy-db run-unit-test kill-db

view-coverage: $(REPORTS)/coverage.out
	go tool cover -html=$(REPORTS)/coverage.out

$(REPORTS):
	-mkdir -p $(REPORTS)

$(REPORTS)/coverage.out:
	unit-test

.PHONY: tools flightctl-server-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

# include the deployment targets
include deploy/deploy.mk
include deploy/agent-vm.mk

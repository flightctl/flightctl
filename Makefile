GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))
TIMEOUT ?= 30m

VERBOSE ?= false

.EXPORT_ALL_VARIABLES:

all: build

help:
	@echo "Targets:"
	@echo "    generate:        regenerate all generated files"
	@echo "    tidy:            tidy go mod"
	@echo "    lint:            run golangci-lint"
	@echo "    build:           run all builds"
	@echo "    integration-test: run integration tests"
	@echo "    unit-test:       run unit tests"
	@echo "    test:            run all tests"
	@echo "    deploy:          deploy flightctl-server and db as pods in kind"
	@echo "    deploy-db:       deploy only the database as a container, for testing"
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
bin/.flightctl-server-container: bin bin/go-cache Containerfile go.mod go.sum $(shell find ./ -name "*.go" -not -path "./bin/*" -not -path "./packaging/*")
	podman build -f Containerfile -v $(shell pwd)/bin/go-cache:/opt/app-root/src/go:Z -t flightctl-server:latest
	touch bin/.flightctl-server-container

flightctl-server-container: bin/.flightctl-server-container

bin:
	mkdir -p bin

# used for caching go container builds download
bin/go-cache:
	mkdir -p bin/go-cache

# only trigger the rpm build when not built before or changes happened to the codebase
bin/.rpm: bin $(shell find ./ -name "*.go" -not -path "./packaging/*") packaging/rpm/flightctl-agent.spec packaging/systemd/flightctl-agent.service hack/build_rpms.sh
	./hack/build_rpms.sh
	touch bin/.rpm

rpm: bin/.rpm

.PHONY: rpm

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

.PHONY: tools flightctl-server-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

# include the deployment targets
include deploy/deploy.mk
include deploy/agent-vm.mk
include test/test.mk

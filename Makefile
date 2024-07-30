GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))
GO_FILES := $(shell find ./ -name ".go" -not -path "./bin" -not -path "./packaging/*")
GO_CACHE := -v $${HOME}/go/flightctl-go-cache:/opt/app-root/src/go:Z -v $${HOME}/go/flightctl-go-cache/.cache:/opt/app-root/src/.cache:Z
TIMEOUT ?= 30m
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

VERBOSE ?= false

SOURCE_GIT_TAG ?=$(shell git describe --always --long --tags --abbrev=7 --match 'v[0-9]*' || echo 'v0.0.0-unknown-$(SOURCE_GIT_COMMIT)')
SOURCE_GIT_TREE_STATE ?=$(shell ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty')
SOURCE_GIT_COMMIT ?=$(shell git rev-parse --short "HEAD^{commit}" 2>/dev/null)
BIN_TIMESTAMP ?=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
MAJOR := $(shell echo $(SOURCE_GIT_TAG) | awk -F'[._~-]' '{print $$1}')
MINOR := $(shell echo $(SOURCE_GIT_TAG) | awk -F'[._~-]' '{print $$2}')
PATCH := $(shell echo $(SOURCE_GIT_TAG) | awk -F'[._~-]' '{print $$3}')

GO_LD_FLAGS := -ldflags "\
	-X github.com/flightctl/flightctl/pkg/version.majorFromGit=$(MAJOR) \
	-X github.com/flightctl/flightctl/pkg/version.minorFromGit=$(MINOR) \
	-X github.com/flightctl/flightctl/pkg/version.patchFromGit=$(PATCH) \
	-X github.com/flightctl/flightctl/pkg/version.versionFromGit=$(SOURCE_GIT_TAG) \
	-X github.com/flightctl/flightctl/pkg/version.commitFromGit=$(SOURCE_GIT_COMMIT) \
	-X github.com/flightctl/flightctl/pkg/version.gitTreeState=$(SOURCE_GIT_TREE_STATE) \
	-X github.com/flightctl/flightctl/pkg/version.buildDate=$(BIN_TIMESTAMP) \
	$(LD_FLAGS)"
GO_BUILD_FLAGS += $(GO_LD_FLAGS)

.EXPORT_ALL_VARIABLES:

all: build build-containers

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
	@echo "    deploy-mq:       deploy only the message queue broker as a container"
	@echo "    clean:           clean up all containers and volumes"
	@echo "    cluster:         create a kind cluster and load the flightctl-server image"
	@echo "    clean-cluster:   kill the kind cluster only"
	@echo "    rpm/deb:         generate rpm or debian packages"

publish: build-containers
	hack/publish_containers.sh

.PHONY: publish

generate:
	go generate -v $(shell go list ./...)
	hack/mockgen.sh

generate-grpc:
	hack/grpcgen.sh

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

lint: tools
	$(GOBIN)/golangci-lint run -v

build: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/...

build-api: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-api

build-worker: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-worker

build-periodic: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-periodic


# rebuild container only on source changes
bin/.flightctl-api-container: bin Containerfile.api go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.api $(GO_CACHE) -t flightctl-api:latest
	touch bin/.flightctl-api-container

bin/.flightctl-worker-container: bin Containerfile.worker go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.worker $(GO_CACHE) -t flightctl-worker:latest
	touch bin/.flightctl-worker-container

bin/.flightctl-periodic-container: bin Containerfile.periodic go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.periodic $(GO_CACHE) -t flightctl-periodic:latest
	touch bin/.flightctl-periodic-container

flightctl-api-container: bin/.flightctl-api-container

flightctl-worker-container: bin/.flightctl-worker-container

flightctl-periodic-container: bin/.flightctl-periodic-container


build-containers: flightctl-api-container flightctl-worker-container flightctl-periodic-container

.PHONY: build-containers


update-server-container: bin/.flightctl-server-container
	kind load docker-image localhost/flightctl-server:latest
	kubectl delete pod -l flightctl.service=flightctl-server -n flightctl-external
	kubectl rollout status deployment flightctl-server -n flightctl-external -w --timeout=30s
	kubectl logs -l flightctl.service=flightctl-server -n flightctl-external -f
bin:
	mkdir -p bin

# only trigger the rpm build when not built before or changes happened to the codebase
bin/.rpm: bin $(shell find ./ -name "*.go" -not -path "./packaging/*") packaging/rpm/flightctl.spec packaging/systemd/flightctl-agent.service hack/build_rpms.sh
	./hack/build_rpms.sh
	touch bin/.rpm

rpm: bin/.rpm

.PHONY: rpm build build-api build-periodic build-worker

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

clean: clean-agent-vm clean-e2e-agent-images
	- kind delete cluster
	- podman-compose -f deploy/podman/compose.yaml down
	- rm -r ~/.flightctl
	- rm -f -r bin
	- rm -f -r $(shell uname -m)
	- rm -f -r obj-*-linux-gnu
	- rm -f -r debian

.PHONY: tools flightctl-api-container flightctl-worker-container flightctl-periodic-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

# include the deployment targets
include deploy/deploy.mk
include deploy/agent-vm.mk
include test/test.mk
include test/scripts/agent-images/agent-images.mk

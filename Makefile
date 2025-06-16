GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin/
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))
GO_FILES := $(shell find ./ -name "*.go" -not -path "./bin" -not -path "./packaging/*")
GO_CACHE := -v $${HOME}/go/flightctl-go-cache:/opt/app-root/src/go:Z -v $${HOME}/go/flightctl-go-cache/.cache:/opt/app-root/src/.cache:Z
TIMEOUT ?= 30m
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

VERBOSE ?= false

SOURCE_GIT_TAG ?=$(shell git describe --tags --exclude latest)
SOURCE_GIT_TREE_STATE ?=$(shell ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty')
SOURCE_GIT_COMMIT ?=$(shell git rev-parse --short "HEAD^{commit}" 2>/dev/null)
BIN_TIMESTAMP ?=$(shell date +'%Y%m%d')
SOURCE_GIT_TAG_NO_V := $(shell echo $(SOURCE_GIT_TAG) | sed 's/^v//')
MAJOR := $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$1}')
MINOR := $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$2}')
PATCH := $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$3}')

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

.PHONY: help
help:
	@echo "Targets:"
	@echo "    generate:        regenerate all generated files"
	@echo "    tidy:            tidy go mod"
	@echo "    lint:            run golangci-lint"
	@echo "    lint-openapi:    run spectral to lint and rulecheck the OpenAPI spec"
	@echo "    lint-docs:       run markdownlint on documentation"
	@echo "    lint-diagrams:   verify that diagrams from Excalidraw have the source code embedded"
	@echo "    spellcheck-docs: run markdown-spellcheck on documentation"
	@echo "    fix-spelling:    run markdown-spellcheck interactively to fix spelling issues"
	@echo "    build:           run all builds"
	@echo "    integration-test: run integration tests"
	@echo "    unit-test:       run unit tests"
	@echo "    test:            run all tests"
	@echo "    deploy:          deploy flightctl-server and db as pods in kind"
	@echo "    redeploy-*       redeploy the api,worker,periodic,alert-exporter containers in kind"
	@echo "    deploy-db:       deploy only the database as a container, for testing"
	@echo "    deploy-mq:       deploy only the message queue broker as a container"
	@echo "    deploy-quadlets: deploy the Flight Control service using Quadlets"
	@echo "    clean:           clean up all containers and volumes"
	@echo "    cluster:         create a kind cluster and load the flightctl-server image"
	@echo "    clean-cluster:   kill the kind cluster only"
	@echo "    clean-quadlets:  clean up all systemd services and quadlet files"
	@echo "    rpm/deb:         generate rpm or debian packages"

.PHONY: publish
publish: build-containers
	hack/publish_containers.sh

generate:
	go generate -v $(shell go list ./... | grep -v -e api/grpc)

generate-proto:
	go generate -v ./api/grpc/...

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy -v'

build: bin build-cli
	CGO_CFLAGS='-flto' GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) \
		./cmd/devicesimulator \
		./cmd/flightctl-agent \
		./cmd/flightctl-api \
		./cmd/flightctl-periodic \
		./cmd/flightctl-worker \
		./cmd/flightctl-alert-exporter

bin/flightctl-agent: bin $(GO_FILES)
	CGO_CFLAGS='-flto' GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) \
		./cmd/flightctl-agent

build-cli: bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl

build-multiarch-clis: bin
	./hack/build_multiarch_clis.sh

build-agent: bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-agent

build-api: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-api

build-worker: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-worker

build-periodic: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-periodic

build-alert-exporter: bin
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-alert-exporter

# rebuild container only on source changes
bin/.flightctl-api-container: bin Containerfile.api go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.api $(GO_CACHE) -t flightctl-api:latest
	touch bin/.flightctl-api-container

bin/.flightctl-worker-container: bin Containerfile.worker go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.worker $(GO_CACHE) -t flightctl-worker:latest
	touch bin/.flightctl-worker-container

bin/.flightctl-periodic-container: bin Containerfile.periodic go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.periodic $(GO_CACHE) -t flightctl-periodic:latest
	touch bin/.flightctl-periodic-container

bin/.flightctl-alert-exporter-container: bin Containerfile.alert-exporter go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.alert-exporter $(GO_CACHE) -t flightctl-alert-exporter:latest
	touch bin/.flightctl-alert-exporter-container

bin/.flightctl-multiarch-cli-container: bin Containerfile.cli-artifacts go.mod go.sum $(GO_FILES)
	mkdir -p $${HOME}/go/flightctl-go-cache/.cache
	podman build -f Containerfile.cli-artifacts $(GO_CACHE) -t flightctl-cli-artifacts:latest
	touch bin/.flightctl-multiarch-cli-container

flightctl-api-container: bin/.flightctl-api-container

flightctl-worker-container: bin/.flightctl-worker-container

flightctl-periodic-container: bin/.flightctl-periodic-container

flightctl-alert-exporter-container: bin/.flightctl-alert-exporter-container

flightctl-multiarch-cli-container: bin/.flightctl-multiarch-cli-container

build-containers: flightctl-api-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-multiarch-cli-container

.PHONY: build-containers build-cli build-multiarch-clis


bin:
	mkdir -p bin

# only trigger the rpm build when not built before or changes happened to the codebase
bin/.rpm: bin $(shell find ./ -name "*.go" -not -path "./packaging/*") packaging/rpm/flightctl.spec packaging/systemd/flightctl-agent.service hack/build_rpms.sh
	./hack/build_rpms.sh
	touch bin/.rpm

rpm: bin/.rpm

.PHONY: rpm build build-api build-periodic build-worker build-alert-exporter

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

clean: clean-agent-vm clean-e2e-agent-images clean-quadlets
	- kind delete cluster
	- rm -r ~/.flightctl
	- rm -f -r bin
	- rm -f -r $(shell uname -m)
	- rm -f -r obj-*-linux-gnu
	- rm -f -r debian

clean-quadlets:
	sudo deploy/scripts/clean_quadlets.sh

.PHONY: tools flightctl-api-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.61.0

lint: tools
	$(GOBIN)/golangci-lint run -v

.PHONY: lint-openapi
lint-openapi:
	@echo "Linting OpenAPI spec"
	podman run --rm -it -v $(shell pwd):/workdir:Z docker.io/stoplight/spectral:6.14.2 lint --ruleset=/workdir/.spectral.yaml --fail-severity=warn /workdir/api/v1alpha1/openapi.yaml

.PHONY: lint-docs
lint-docs:
	@echo "Linting user documentation markdown files"
	podman run --rm -v $(shell pwd):/workdir:Z docker.io/davidanson/markdownlint-cli2:v0.16.0 "docs/user/**/*.md"

.PHONY: lint-diagrams
lint-diagrams:
	@echo "Verifying Excalidraw diagrams have scene embedded"
	@for d in $$(find . -type d); do \
		for f in $$(find $$d -maxdepth 1 -type f -iname '*.svg'); do \
			if [ -f "$$d/.excalidraw-ignore" ] && $$(basename "$$f" | grep -q --basic-regexp --file=$$d/.excalidraw-ignore); then continue ; fi ; \
			if ! grep -q "excalidraw+json" $$f; then \
				echo "$$f was not exported from excalidraw with 'Embed Scene' enabled." ; \
				echo "If this is not an excalidraw file, add it to $$d/.excalidraw-ignore" ; \
				exit 1 ; \
			fi ; \
		done ; \
	done

.PHONY: spellcheck-docs
spellcheck-docs:
	@echo "Checking user documentation for spelling issues"
	podman run --rm -v $(shell pwd):/workdir:Z docker.io/tmaier/markdown-spellcheck:latest --en-us --ignore-numbers --report "docs/user/**/*.md"

.PHONY: fix-spelling
fix-spelling:
	@echo "Running markdown-spellcheck interactively to allow fixing spelling issues"
	podman run --rm -it -v $(shell pwd):/workdir:Z docker.io/tmaier/markdown-spellcheck:latest --en-us --ignore-numbers "docs/user/**/*.md"

# include the deployment targets
include deploy/deploy.mk
include deploy/agent-vm.mk
include test/test.mk
include test/scripts/agent-images/agent-images.mk

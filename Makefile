# Enable BuildKit for all container buildsmakefi
export DOCKER_BUILDKIT=1
export BUILDKIT_PROGRESS=plain

# --- Container Registry Configuration ---
# Use environment variables if they exist, otherwise use these defaults.
# This allows the CI pipeline to easily override them.
REGISTRY       ?= localhost
REGISTRY_OWNER ?= flightctl
REGISTRY_OWNER_TESTS ?= flightctl-tests
GITHUB_ACTIONS ?= false

# --- Cache Configuration ---
# Always use caching with localhost defaults for local builds
# In CI, REGISTRY and REGISTRY_OWNER will be overridden
CACHE_FLAGS_TEMPLATE := --cache-from=$(REGISTRY)/$(REGISTRY_OWNER)/%

# Function to generate cache flags for a specific image
# Only returns cache flags if GITHUB_ACTIONS is true
 ifeq ($(GITHUB_ACTIONS),true)
 define CACHE_FLAGS_FOR_IMAGE
 $(subst %,$(1),$(CACHE_FLAGS_TEMPLATE))
 endef
 else
 define CACHE_FLAGS_FOR_IMAGE

 endef
 endif



ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))
GOBASE=$(ROOT_DIR)
GOBIN=$(ROOT_DIR)/bin/
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
GO_FILES = $(shell find $(ROOT_DIR)/ -name "*.go" -not -path "$(ROOT_DIR)/bin" -not -path "$(ROOT_DIR)/packaging/*")
TIMEOUT ?= 30m
GOOS = $(shell go env GOOS)
GOARCH = $(shell go env GOARCH)
RPM_MOCK_ROOT_DEFAULT = centos-stream+epel-next-9-x86_64

VERBOSE ?= false

SOURCE_GIT_TAG ?=$(shell $(ROOT_DIR)/hack/current-version)
SOURCE_GIT_TREE_STATE ?=$(shell ( ( [ ! -d "$(ROOT_DIR)/.git/" ] || git -C $(ROOT_DIR) diff --quiet ) && echo 'clean' ) || echo 'dirty')
SOURCE_GIT_COMMIT ?=$(shell git -C $(ROOT_DIR) rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")
BIN_TIMESTAMP ?=$(shell date +'%Y%m%d')
SOURCE_GIT_TAG_NO_V = $(shell echo $(SOURCE_GIT_TAG) | sed 's/^v//')
MAJOR = $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$1}')
MINOR = $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$2}')
PATCH = $(shell echo $(SOURCE_GIT_TAG_NO_V) | awk -F'[._~-]' '{print $$3}')

# If a FIPS-validated Go toolset is found, build in FIPS mode unless explicitly disabled by the user using DISABLE_FIPS="true"
FIPS_VALIDATED_TOOLSET = $(shell go env GOVERSION | grep -q "Red Hat" && echo "true" || echo "false")
GOENV = $(if $(filter true,$(DISABLE_FIPS)),CGO_ENABLED=0,$(if $(filter true,$(FIPS_VALIDATED_TOOLSET)),CGO_ENABLED=1 CGO_CFLAGS=-flto GOEXPERIMENT=strictfipsruntime,CGO_ENABLED=0))

ifeq ($(DEBUG),true)
	# throw all the debug info in!
	LD_FLAGS :=
	GC_FLAGS := -gcflags "all=-N -l"
else
	# strip debug info, but keep symbols
	LD_FLAGS := -w
	GC_FLAGS :=
endif

GO_LD_FLAGS = $(GC_FLAGS) -ldflags "\
	-X github.com/flightctl/flightctl/pkg/version.majorFromGit=$(MAJOR) \
	-X github.com/flightctl/flightctl/pkg/version.minorFromGit=$(MINOR) \
	-X github.com/flightctl/flightctl/pkg/version.patchFromGit=$(PATCH) \
	-X github.com/flightctl/flightctl/pkg/version.versionFromGit=$(SOURCE_GIT_TAG) \
	-X github.com/flightctl/flightctl/pkg/version.commitFromGit=$(SOURCE_GIT_COMMIT) \
	-X github.com/flightctl/flightctl/pkg/version.gitTreeState=$(SOURCE_GIT_TREE_STATE) \
	-X github.com/flightctl/flightctl/pkg/version.buildDate=$(BIN_TIMESTAMP) \
	$(LD_FLAGS)"
GO_BUILD_FLAGS += $(GO_LD_FLAGS)

# Removed .EXPORT_ALL_VARIABLES as it forces evaluation of all variables (even lazy ones)
# causing massive slowdown. Export only what's needed explicitly above.

all: build build-containers

.PHONY: help
help:
	@echo "Targets:"
	@echo "    generate:        regenerate all generated files"
	@echo "    tidy:            tidy go mod"
	@echo "    lint:            run golangci-lint"
	@echo "    rpmlint:         run rpmlint on RPM spec file"
	@echo "    lint-openapi:    run spectral to lint and rulecheck the OpenAPI spec"
	@echo "    lint-docs:       run markdownlint on documentation"
	@echo "    lint-diagrams:   verify that diagrams from Excalidraw have the source code embedded"
	@echo "    lint-helm:       run helm lint"
	@echo "    spellcheck-docs: run markdown-spellcheck on documentation"
	@echo "    fix-spelling:    run markdown-spellcheck interactively to fix spelling issues"
	@echo "    build:           run all builds"
	@echo "    integration-test: run integration tests"
	@echo "    unit-test:       run unit tests"
	@echo "    test:            run all tests"
	@echo "    deploy:          deploy flightctl-server and db as pods in kind"
	@echo "    redeploy-*       redeploy the api,worker,periodic,alert-exporter containers in kind"
	@echo "    deploy-db:       deploy only the database as a container, for testing"
	@echo "    deploy-kv:       deploy only the key-value store as a container, for testing"
	@echo "    deploy-quadlets: deploy the complete Flight Control service using Quadlets"
	@echo "                     (includes proper startup ordering: DB -> KV -> other services)"
	@echo "    clean:           clean up all containers and volumes"
	@echo "    clean-all:       full cleanup including containers and bin directory"
	@echo "    clean-e2e-images: clean up e2e test images (app and device) from both regular and root podman"
	@echo "    rebuild-containers: force rebuild all containers"
	@echo "    bundle-containers: bundle all flightctl containers into tar archive"
	@echo "    cluster:         create a kind cluster and load the flightctl-server image"
	@echo "    clean-cluster:   kill the kind cluster only"
	@echo "    clean-quadlets:  clean up all systemd services and quadlet files"
	@echo "    rpm/deb:         generate rpm or debian packages"
	@echo ""
	@echo "CI/CD Targets:"
	@echo "    login:           login to container registry (requires REGISTRY_USER env var)"
	@echo "    push-containers: push all containers to registry"
	@echo "    ci-build:        full CI build and push process"
	@echo ""
	@echo "Environment Variables for CI:"
	@echo "    REGISTRY:        container registry (default: localhost)"
	@echo "    REGISTRY_OWNER:  registry owner/organization (default: flightctl)"
	@echo "    REGISTRY_OWNER_TESTS:  test registry owner/organization (default: flightctl-tests)"
	@echo "    REGISTRY_USER:   registry username for login"
	@echo "    GITHUB_ACTIONS:  set to 'true' to enable container build caching"
	@echo ""
	@echo "Caching Strategy:"
	@echo "    Uses GitHub Actions cache (type=gha) for CI builds"
	@echo "    Cache flags only added when GITHUB_ACTIONS=true"

.PHONY: publish
publish: build-containers
	hack/publish_containers.sh

generate:
	go generate -v $(shell go list ./... | grep -v -e api/grpc)

generate-proto:
	go generate -v ./api/grpc/...

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy -v'

build: bin build-cli build-pam-issuer
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) \
		./cmd/devicesimulator \
		./cmd/flightctl-agent \
		./cmd/flightctl-api \
		./cmd/flightctl-periodic \
		./cmd/flightctl-worker \
		./cmd/flightctl-alert-exporter \
		./cmd/flightctl-alertmanager-proxy \
		./cmd/flightctl-userinfo-proxy \
		./cmd/flightctl-db-migrate \
		./cmd/flightctl-restore \
		./cmd/flightctl-telemetry-gateway \
		./cmd/flightctl-standalone

bin/flightctl-agent: bin $(GO_FILES)
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-agent

build-cli: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl

build-multiarch-clis: bin
	./hack/build_multiarch_clis.sh

build-agent: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-agent

build-api: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-api

build-pam-issuer: bin
	$(GOENV) GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=1 CGO_CFLAGS="$$CGO_CFLAGS -D_GNU_SOURCE" CGO_LDFLAGS="-ldl" go build -tags linux -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-pam-issuer

build-db-migrate: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-db-migrate

build-restore: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-restore

build-worker: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-worker

build-periodic: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-periodic

build-alert-exporter: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-alert-exporter

build-alertmanager-proxy: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-alertmanager-proxy

build-userinfo-proxy: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-userinfo-proxy

build-telemetry-gateway: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-telemetry-gateway

build-devicesimulator: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/devicesimulator

build-standalone: bin
	$(GOENV) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/flightctl-standalone

# Container builds - Environment-aware caching
flightctl-api-container: Containerfile.api go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-api) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.api -t flightctl-api:latest -t quay.io/flightctl/flightctl-api:$(SOURCE_GIT_TAG)

flightctl-pam-issuer-container: Containerfile.pam-issuer go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-pam-issuer) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.pam-issuer -t flightctl-pam-issuer:latest -t quay.io/flightctl/flightctl-pam-issuer:$(SOURCE_GIT_TAG)

flightctl-db-setup-container: Containerfile.db-setup deploy/scripts/setup_database_users.sh deploy/scripts/setup_database_users.sql
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-db-setup) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.db-setup \
		-t flightctl-db-setup:latest -t quay.io/flightctl/flightctl-db-setup:$(SOURCE_GIT_TAG) .

flightctl-worker-container: Containerfile.worker go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-worker) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.worker -t flightctl-worker:latest -t quay.io/flightctl/flightctl-worker:$(SOURCE_GIT_TAG)

flightctl-periodic-container: Containerfile.periodic go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-periodic) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.periodic -t flightctl-periodic:latest -t quay.io/flightctl/flightctl-periodic:$(SOURCE_GIT_TAG)

flightctl-alert-exporter-container: Containerfile.alert-exporter go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-alert-exporter) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.alert-exporter -t flightctl-alert-exporter:latest -t quay.io/flightctl/flightctl-alert-exporter:$(SOURCE_GIT_TAG)

flightctl-alertmanager-proxy-container: Containerfile.alertmanager-proxy go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-alertmanager-proxy) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.alertmanager-proxy -t flightctl-alertmanager-proxy:latest -t quay.io/flightctl/flightctl-alertmanager-proxy:$(SOURCE_GIT_TAG)

flightctl-multiarch-cli-container: Containerfile.cli-artifacts go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-cli-artifacts) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.cli-artifacts -t flightctl-cli-artifacts:latest -t quay.io/flightctl/flightctl-cli-artifacts:$(SOURCE_GIT_TAG)

flightctl-userinfo-proxy-container: Containerfile.userinfo-proxy go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-userinfo-proxy) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.userinfo-proxy -t flightctl-userinfo-proxy:latest -t quay.io/flightctl/flightctl-userinfo-proxy:$(SOURCE_GIT_TAG)

flightctl-telemetry-gateway-container: Containerfile.telemetry-gateway go.mod go.sum $(GO_FILES)
	podman build $(call CACHE_FLAGS_FOR_IMAGE,flightctl-telemetry-gateway) \
		--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG} \
		--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE} \
		--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT} \
		-f Containerfile.telemetry-gateway -t flightctl-telemetry-gateway:latest -t quay.io/flightctl/flightctl-telemetry-gateway:$(SOURCE_GIT_TAG)

.PHONY: flightctl-api-container flightctl-pam-issuer-container flightctl-db-setup-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-alertmanager-proxy-container flightctl-multiarch-cli-container flightctl-userinfo-proxy-container flightctl-telemetry-gateway-container

# --- Registry Operations ---
# The login target expects REGISTRY_USER via environment variable and
# REGISTRY_PASSWORD via standard input for security.
login:
	@echo "--- Logging into registry: $(REGISTRY) ---"
	@if [ -z "$(REGISTRY_USER)" ]; then \
		echo "Error: REGISTRY_USER environment variable not set."; \
		exit 1; \
	fi
	@echo "Piping password to podman login for user $(REGISTRY_USER)..."
	@cat /dev/stdin | podman login -u "$(REGISTRY_USER)" --password-stdin $(REGISTRY)

# Push all containers to registry
push-containers: login
	@echo "--- Pushing all containers to registry ---"
	podman push flightctl-api:latest
	podman push flightctl-pam-issuer:latest
	podman push flightctl-db-setup:latest
	podman push flightctl-worker:latest
	podman push flightctl-periodic:latest
	podman push flightctl-alert-exporter:latest
	podman push flightctl-alertmanager-proxy:latest
	podman push flightctl-cli-artifacts:latest
	podman push flightctl-userinfo-proxy:latest
	podman push flightctl-telemetry-gateway:latest

# A convenience target to run the full CI process.
ci-build: build-containers push-containers
	@echo "--- CI Build & Push Complete ---"

# Force rebuild all containers
rebuild-containers: clean-containers build-containers

# Clean only containers (preserve cluster and other artifacts)
clean-containers:
	- podman rmi flightctl-api:latest || true
	- podman rmi flightctl-pam-issuer:latest || true
	- podman rmi flightctl-db-setup:latest || true
	- podman rmi flightctl-worker:latest || true
	- podman rmi flightctl-periodic:latest || true
	- podman rmi flightctl-alert-exporter:latest || true
	- podman rmi flightctl-alertmanager-proxy:latest || true
	- podman rmi flightctl-cli-artifacts:latest || true
	- podman rmi flightctl-userinfo-proxy:latest || true
	- podman rmi flightctl-telemetry-gateway:latest || true

build-containers: flightctl-api-container flightctl-pam-issuer-container flightctl-db-setup-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-alertmanager-proxy-container flightctl-multiarch-cli-container flightctl-userinfo-proxy-container flightctl-telemetry-gateway-container

bundle-containers:
	test/scripts/agent-images/scripts/bundle.sh \
		--image-pattern 'quay.io/flightctl/.*:$(SOURCE_GIT_TAG)' \
		--output-path 'bin/flightctl-images-bundle.tar'

.PHONY: build-containers bundle-containers build-cli build-multiarch-clis


bin:
	mkdir -p bin

# only trigger the rpm build when not built before or changes happened to the codebase
bin/.rpm: $(shell find $(ROOT_DIR)/ -name "*.go" -not -path "$(ROOT_DIR)/packaging/*") \
          packaging/rpm/flightctl.spec \
          packaging/systemd/flightctl-agent.service \
          hack/build_rpms.sh \
          $(shell find $(ROOT_DIR)/packaging/selinux -type f) \
          | bin
	@sudo GOMODCACHE="$(shell go env GOMODCACHE)" \
	     GOCACHE="$(shell go env GOCACHE)" \
	     "$(ROOT_DIR)/hack/build_rpms.sh" \
	     --root "$(if $(RPM_MOCK_ROOT),$(RPM_MOCK_ROOT),$(RPM_MOCK_ROOT_DEFAULT))"
	@sudo chown -R $(shell id -u):$(shell id -g) bin/rpm/
	touch bin/.rpm

rpm: bin/.rpm

.PHONY: rpm build build-api build-pam-issuer build-periodic build-worker build-alert-exporter build-alertmanager-proxy build-userinfo-proxy build-standalone

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

clean: clean-agent-vm clean-e2e-agent-images clean-quadlets clean-swtpm-certs
	- kind delete cluster
	- rm -rf ~/.flightctl
	- rm -rf $(shell uname -m)
	- rm -rf obj-*-linux-gnu
	- rm -rf debian
	- rm -rf .output/stamps
	- rm -f bin/flightctl-images-bundle.tar
# Full cleanup including bin directory and all artifacts
clean-all: clean clean-containers
	- rm -rf bin

clean-quadlets:
	sudo deploy/scripts/clean_quadlets.sh


.PHONY: tools flightctl-api-container flightctl-pam-issuer-container flightctl-db-setup-container flightctl-worker-container flightctl-periodic-container flightctl-alert-exporter-container flightctl-userinfo-proxy-container flightctl-telemetry-gateway-container

# Use custom golangci-lint container with libvirt support
LINT_IMAGE := flightctl-lint:latest
LINT_CONTAINER := podman run --rm \
	-v $(GOBASE):/app:Z \
	-v golangci-lint-cache:/root/.cache/golangci-lint \
	-v go-build-cache:/root/.cache/go-build \
	-v go-mod-cache:/go/pkg/mod \
	-w /app --user 0 $(LINT_IMAGE)

.PHONY: tools
tools:

.output/stamps/lint-image: Containerfile.lint go.mod go.sum
	@mkdir -p .output/stamps
	podman build -f Containerfile.lint -t $(LINT_IMAGE)
	@touch .output/stamps/lint-image

.PHONY: lint
lint: .output/stamps/lint-image
	$(LINT_CONTAINER) golangci-lint run -v

.PHONY: rpmlint
rpmlint: check-rpmlint
	@echo "Running rpmlint on RPM spec file"
	rpmlint packaging/rpm/flightctl.spec

.PHONY: rpmlint-ci
rpmlint-ci:
	@echo "Running rpmlint on RPM spec file (CI mode)"
	rpmlint packaging/rpm/flightctl.spec

.PHONY: check-rpmlint
check-rpmlint:
	@command -v rpmlint > /dev/null || (echo "rpmlint not found. Install with: sudo apt-get install rpmlint (Ubuntu/Debian) or sudo dnf install rpmlint (Fedora/RHEL)" && exit 1)

.output/stamps/lint-openapi: api/v1beta1/openapi.yaml .spectral.yaml
	@mkdir -p .output/stamps
	@echo "Linting OpenAPI spec"
	podman run --rm -it -v $(shell pwd):/workdir:Z docker.io/stoplight/spectral:6.14.2 lint --ruleset=/workdir/.spectral.yaml --fail-severity=warn /workdir/api/v1beta1/openapi.yaml
	@touch .output/stamps/lint-openapi

.PHONY: lint-openapi
lint-openapi: .output/stamps/lint-openapi

.PHONY: lint-helm
lint-helm:
	helm lint deploy/helm/flightctl --values deploy/helm/flightctl/lint-values.yaml

.output/stamps/lint-docs: $(wildcard docs/user/*.md)
	@mkdir -p .output/stamps
	@echo "Linting user documentation markdown files"
	podman run --rm -v $(shell pwd):/workdir:Z docker.io/davidanson/markdownlint-cli2:v0.19.0 "docs/user/**/*.md"
	@touch .output/stamps/lint-docs

.PHONY: lint-docs
lint-docs: .output/stamps/lint-docs

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

.output/stamps/spellcheck-docs: $(wildcard docs/user/*.md)
	@mkdir -p .output/stamps
	@echo "Checking user documentation for spelling issues"
	podman run --rm -v $(shell pwd):/workdir:Z docker.io/tmaier/markdown-spellcheck:latest --en-us --ignore-numbers --report "docs/user/**/*.md"
	@touch .output/stamps/spellcheck-docs

.PHONY: spellcheck-docs
spellcheck-docs: .output/stamps/spellcheck-docs

.PHONY: fix-spelling
fix-spelling:
	@echo "Running markdown-spellcheck interactively to allow fixing spelling issues"
	podman run --rm -it -v $(shell pwd):/workdir:Z docker.io/tmaier/markdown-spellcheck:latest --en-us --ignore-numbers "docs/user/**/*.md"

# include the deployment targets
include deploy/deploy.mk
include deploy/agent-vm.mk
include test/test.mk
include test/scripts/agent-images/agent-images.mk

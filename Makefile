GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GO_BUILD_FLAGS := ${GO_BUILD_FLAGS}
ROOT_DIR := $(or ${ROOT_DIR},$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST)))))

VERBOSE ?= false
REPORTS ?= $(ROOT_DIR)/reports

GO_TEST_FORMAT = pkgname
ifeq ($(VERBOSE), true)
        GO_TEST_FORMAT=standard-verbose
endif

GINKGO_REPORTFILE := $(or $(GINKGO_REPORTFILE), ./junit_unit_test.xml)
GO_UNITTEST_FLAGS = --format=$(GO_TEST_FORMAT) $(GOTEST_PUBLISH_FLAGS) -- -count=1 -coverprofile=coverage.out $(GO_BUILD_FLAGS)
GINKGO_UNITTEST_FLAGS = -ginkgo.focus="$(FOCUS)" -ginkgo.v -ginkgo.skip="$(SKIP)" -ginkgo.v -ginkgo.reportFile=$(GINKGO_REPORTFILE)

.EXPORT_ALL_VARIABLES:

all: build

help:
	@echo "Targets:"
	@echo "    generate:        regenerate all generated files"
	@echo "    tidy:            tidy go mod"
	@echo "    lint:            run golangci-lint"
	@echo "    build:           run all builds"
	@echo "    unit-test:       run unit tests"
	@echo "    deploy:          deploy flightctl-server and db as containers in podman"
	@echo "    deploy-db:       deploy only the database as a container in podman"
	@echo "    clean:           clean up all containers and volumes"
	@echo "    rpm/deb:         generate rpm or debian packages"

generate:
	find . -name 'mock_*.go' -type f -not -path './vendor/*' -delete
	go generate -v $(shell go list ./...)

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

ci-lint:
	${ROOT_DIR}/hack/check-commits.sh
	$(MAKE) generate
	git diff --exit-code  # this will fail if code generation caused any diff

lint: tools
	$(GOBIN)/golangci-lint run -v

build: bin
	go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/...

flightctl-server-container:
	podman build -f Containerfile -t flightctl-server:latest

deploy-db:
	cd deploy/podman && podman-compose up -d flightctl-db

deploy: build flightctl-server-container
	cd deploy/podman && podman-compose up -d
	podman cp flightctl-server:/root/.flightctl "${HOME}"

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

clean:
	- podman-compose -f deploy/podman/compose.yaml down
	- podman-compose -f deploy/podman/observability.yaml down
	- rm -r ~/.flightctl
	- podman volume ls | grep local | awk '{print $$2}' | xargs podman volume rm
	- rm -f -r bin
	- rm -f -r $(shell uname -m)
	- rm -f -r obj-*-linux-gnu
	- rm -f -r debian

_unit_test: $(REPORTS)
	gotestsum $(GO_UNITTEST_FLAGS) $(TEST) $(GINKGO_UNITTEST_FLAGS) -timeout $(TIMEOUT) || ($(MAKE) _post_unit_test && /bin/false)
	$(MAKE) _post_unit_test

_post_unit_test: $(REPORTS)
	@for name in `find '$(ROOT_DIR)' -name 'junit*.xml' -type f -not -path '$(REPORTS)/*'`; do \
		mv -f $$name $(REPORTS)/junit_unit_$$(basename $$(dirname $$name)).xml; \
	done

run-unit-test:
	SKIP_UT_DB=1 $(MAKE) _unit_test TIMEOUT=30m TEST="$(or $(TEST),$(shell go list ./...))"

run-db-container:
	podman rm -f flightctl-db || true
	podman volume rm podman_flightctl-db || true
	podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec podman_flightctl-db
	cd deploy/podman && podman-compose up -d flightctl-db
	hack/wait_for_postgres.sh
	podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
	podman exec -it flightctl-db createdb admin || true

kill-db-container:
	podman rm -f flightctl-db || true
	podman volume rm podman_flightctl-db || true

unit-test: run-db-container run-unit-test kill-db-container

$(REPORTS):
	-mkdir -p $(REPORTS)

.PHONY: tools deploy deploy-db flightctl-server-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

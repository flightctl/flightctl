GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GO_BUILD_FLAGS :=-tags 'containers_image_openpgp osusergo exclude_graphdriver_btrfs exclude_graphdriver_devicemapper'

help:
	@echo "Targets:"
	@echo "    generate:    regenerate all generated files"
	@echo "    tidy:        tidy go mod"
	@echo "    lint:        run golangci-lint"
	@echo "    build:       run all builds"
	@echo "    test:        run all tests"
	@echo "    deploy:      deploy flightctl-server and db as containers in podman"
	@echo "    deploy-db:   deploy only the database as a container in podman"
	@echo "    clean:       clean up all containers and volumes"

generate:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go generate ./...'

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

lint: tools
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && $(GOBIN)/golangci-lint run ./...'

build: bin
	go build -buildvcs=false $(GO_BUILD_FLAGS) -o $(GOBIN) ./cmd/...

test:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go test -cover ./...'

flightctl-server-container:
	podman build -f Containerfile -t flightctl-server:latest

deploy-db:
	cd deploy/podman && podman-compose up -d flightctl-db

deploy: build flightctl-server-container
	cd deploy/podman && podman-compose up -d
	podman cp flightctl-server:/root/.flightctl "${HOME}"

bin:
	mkdir -p bin

rpm: build
	mkdir -p rpmbuild/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
	mkdir -p bin/flightctl-agent-0.0.1
	cp bin/flightctl-agent bin/flightctl-agent-0.0.1
	cp packaging/systemd/flightctl-agent.service bin/flightctl-agent-0.0.1
	tar cvf rpmbuild/SOURCES/flightctl-agent-0.0.1.tar -C bin/ flightctl-agent-0.0.1
	rpmbuild --define "_topdir $(GOBASE)/rpmbuild" -ba $(GOBASE)/packaging/rpm/flightctl-agent.spec

clean:
	- podman-compose -f deploy/podman/compose.yaml down
	- podman-compose -f deploy/podman/observability.yaml down
	- rm -r ~/.flightctl
	- podman volume ls | grep local | awk '{print $$2}' | xargs podman volume rm
	- rm -r bin
	- rm -r rpmbuild

.PHONY: tools deploy deploy-db flightctl-server-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

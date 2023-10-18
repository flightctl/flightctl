GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin

help:
	@echo "Targets:"
	@echo "    generate:    regenerate all generated files"
	@echo "    tidy:        tidy go mod"
	@echo "    lint:        run golangci-lint"
	@echo "    build:       run all builds"
	@echo "    test:        run all tests"
	@echo "    deploy:      deploy flightctl-server and db as containers in podman"
	@echo "    deploy-db:   deploy only the database as a container in podman"

generate:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go generate ./...'

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

lint: tools
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && $(GOBIN)/golangci-lint run ./...'

build: bin
	go build -buildvcs=false -o $(GOBIN) ./cmd/...

test:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go test -cover ./...'

flightctl-server-container:
	podman build -f Containerfile -t flightctl-server:latest

deploy-db:
	cd deploy/podman && podman-compose up -d flightctl-db

deploy: flightctl-server-container
	cd deploy/podman && podman-compose up -d
	podman cp flightctl-server:/root/.flightctl "${HOME}"

bin:
	mkdir -p bin

.PHONY: tools deploy deploy-db flightctl-server-container
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

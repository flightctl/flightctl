GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin

help:
	@echo "Targets:"
	@echo "    generate:    regenerate all generated files"
	@echo "    tidy:        tidy go mod"
	@echo "    lint:        run golangci-lint"
	@echo "    build:       run all builds"
	@echo "    test:        run all tests"

generate:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go generate ./...'

tidy:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go mod tidy'

lint: tools
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && $(GOBIN)/golangci-lint run ./...'

build:
	go build -o $(GOBIN) ./cmd/...

test:
	git ls-files go.mod '**/*go.mod' -z | xargs -0 -I{} bash -xc 'cd $$(dirname {}) && go test -cover ./...'

.PHONY: tools
tools: $(GOBIN)/golangci-lint

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) v1.54.0

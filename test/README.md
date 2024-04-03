# Testing strategy

This document describes the desired testing strategy for the project, which should
slowly be implemented over time.

This is a live document and should be updated as the project evolves, to cover
details like `api` testing or `benchmarking`.

## Introduction

This project is a complex system with many moving parts. It is important to have a
coherent testing strategy to ensure that the system works as expected,
doesn't break over time, and very importantly, make sure that developers know
where and how to test things.

As testing tools we use two frameworks:

- Ginkgo and Gomega, a BDD-style testing framework for Go, used for more complex
  testing where BeforeEach/AfterEach is beneficial. It has the benefit of allowing
  to write tests in a more human-readable way.

- Go's built-in testing framework with testify, used for simpler unit tests.

## Internal test libraries

We maintain our internal test libraries in `/test/pkg/`

# Types of tests

## Unit testing

Unit tests are used to test individual functions or methods in isolation. They
should be fast, and should not depend on external services or databases.

Cross-package tests can be performed sometimes in our unit testing, as long
as there is no dependency on the database or other services functionality.

We keep the test files for unit tests in the same directory as the code we are
testing.

i.e.

code in `pkg/log/log.go` should have unit tests in `pkg/log/log_test.go`

We use the go unit test framework with testify for unit tests.

Unit tests can be run locally with:
    
```bash
make unit-test
```

### Mocking

Sometimes we need to mock interfaces to make unit testing possible in
isolation. For that we use the `mockgen` tool from `go.uber.org/mock`

If you want to generate mocks for a package, you can add the reference to
`/hack/mock.list.txt` and run `make generate` to generate the mocks.

Find more information about using mockgen [here](https://pkg.go.dev/go.uber.org/mock#readme-building-mocks)


## Integration testing

Tests the interactions between our different software components in a mocked
environment, here we are not testing dependencies with the operating system or external
services (beyond the database) and we are not deploying the components of the system,
but we run instances of our objects from the go tests.

External systems or OS interaction are mocked.

Those tests are stored on a separate directory `/test/integration/<topic>`,
i.e. we can test the following topics:

* agent
* storage
* server-api
* cmdline

We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

Tests made for integration testing can be built with the testing harness
provided in `/test/pkg/harness` which provides an object to test
a server and an agent together, building any necessary crypto material,
providing a test database and a mock directory for the agent to interact with.

can be run with:
```bash
make integration-test # or run-integration-test if you have a DB/deployment ready
```

For mocking specific interfaces please refer to the unit-test mocking section.

### Note on coverage testing

We  run all unit tests and integration testing separately, but we provide a separate
make target that provides unified coverage results by merging coverage output
from unit and integration tests using the go coverage tools.

`make coverage`

## E2E testing

This type of testing verifies the interaction of our software components with
external software or services, such as the operating system, registries,
git repositories, etc.

Our stack helps deploy a complete system with `make deploy` and this stack
should provide everything necessary to perform e2e testing.

We maintain the end-to-end test files in `/test/e2e/<topic>`
directory, ie. `/test/e2e/agent/`, `/test/e2e/api/`, etc.

as examples:

* `agent` contains tests for the agent component in interaction with the OS and
          registries: switching an image, rebooting, failure and rollback, etc.

* `gitops` contains tests for the server that verify interaction
           with external git repositories.

* `k8s/secrets` contains for the server that verify interaction with a k8s API
                in terms of secret retrieval.

E2E tests can be run with our testing harness in `/test/pkg/harness/e2e` which
provides additional functionality on top of `/test/pkg/harness` to interact
with agents on VMs, or connect the server to the local kind k8s cluster.

We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

```bash
make e2e-test
```

## Command line tool testing

Today we test the command line tool using the bash/github actions, we
may want to migrate this under integration testing in the future as described
in the integration testing section.

For more details look at the .github/workflows/pr-smoke-testing.yaml


# Future work

Additional testing will be analyzed in the future, including:

* upgrade testing between versions
* load testing
* scale testing
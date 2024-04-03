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
  testing where BeforeEach/AfterEach is beneficial. It has de benefit of allowing
  to write tests in a more human-readable way.

- Go's built-in testing framework with testify, used for simpler unit tests.

## Internal test libraries

We maintain our internal test libraries in `/test/pkg/`

# Types of tests

## Unit testing

Unit tests are used to test individual functions or methods in isolation. They
should be fast, and should not depend on external services or databases.

We keep the test files for unit tests in the same directory as the code we are
testing.

i.e.

code in `pkg/log/log.go` should have unit tests in `pkg/log/log_test.go`

We generally use testify, and sometimes Ginkgo/gomega, depending on the nature
of the test, the choice here is flexible.

Unit tests can be run locally with:
    
```bash
make unit-test # or run-unit-test if you have a DB server ready
```

### Mocking

Sometimes we need to mock interfaces to make unit testing possible in
isolation. For that we use the `mockgen` tool from `go.uber.org/mock`

If you want to generate mocks for a package, you can add the reference to
`/hack/mock.list.txt` and run `make generate` to generate the mocks.

Find more information about using mockgen [here](https://pkg.go.dev/go.uber.org/mock#readme-building-mocks)


## Integration testing

Tests the interactions between our different software components, here
we are not testing dependencies with the operating system or external
services (beyond the database). External systems or OS interaction
will be mocked.

Those tests are stored on a separate directory `/test/integration/<component>`,
i.e. `/test/integration/agent/`, `/test/integration/cmdline/`, etc.

Currently we only have three components: `agent`, `server` and `cmdline tool`,
but some components are likely to be split in the future (enrollment requests,
a CA component, etc.))

```bash
make integration-test # or run-integration-test if you have a DB/deployment ready
```
We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

Tests made for integration testing can be built with the testing harness
provided in `/test/pkg/harness` which provides an object to test
a server and an agent together, building any necessary crypto material,
providing a test database and a mock directory for the agent to interact with.

## E2E testing

This type of testing hels verify the interaction of our software components with
external software or services, such as the operating system, registries,
git repositories, etc.

Our stack helps deploy a complete system with `make deploy` and this stack
should provide everything necessary to perform e2e testing.

We maintain the test files end-to-end tests in `/test/e2e/<component>`
directory, ie. `/test/e2e/agent/`, `/test/e2e/api/`, etc.

i.e. `/test/e2e/agent` should contain tests for the agent component that
interact with the system: switching an image, rebooting, failure and rollback,
etc.

while `/test/e2e/api` should contain tests for the API component that interact
with other services in a deployment: fetching from a git repository, fetching
secrets from k8s, etc.

E2E tests can be run with our testing harness in `/test/pkg/harness/e2e` which
provides additional functionality on top of `/test/pkg/harness` to interact
with agents on VMs, or connect the server to the local kind k8s cluster.

We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

```bash
make e2e-test
```

## Command line tool testing

Today we test the command line tool using the bash/github actions today, we
may want to migrate this under integration testing in the future as described
in the integration testing section.

For more details look at the .github/workflows/pr-smoke-testing.yaml

# Testing strategy

This document describes the testing strategy for the project.

## Introduction

This project is a complex system with many moving parts. It is important to have a
comprehensive testing strategy to ensure that the system works as expected, and
doesn't break over time.

As testing tools we use two frameworks:

- Ginkgo and Gomega, a BDD-style testing framework for Go, used for more complex
  testing where BeforeEach/AfterEach is beneficial. It has de benefit of allowing
  to write tests in a more human-readable way.

- Go's built-in testing framework with testify, used for simpler unit tests.

## Unit testing

Unit tests are used to test individual functions or methods in isolation. They
should be fast, and should not depend on external services or databases.

We keep the test files for unit tests in the same directory as the code we are
testing.

We generally use testify, and sometimes Ginkgo/gomega, depending on the nature
of the test, the choice here is flexible.

Unit tests can be run locally with:
    
 ```bash
make unit-test # or run-unit-test if you have a DB server ready
```

## API Testing

We maintain the test files for API tests in the `/tests/api/<endpoint>` directory.

API tests are used to test the API endpoints of the system. They should test
the happy path, as well as any edge cases that might cause the system to fail.

API tests should mock any external services that the API depends on, to ensure
that the tests are fast, reliable, and identifying issues within the API itself,
not the external services.

If that is unfeasible for some reason, the tests should be performed in the E2E
testing suite.

API tests can be run locally with:
    
```bash
make api-test # or run-api-test if you have a DB server ready
```

## Command line tool testing

We test the command line tool using the bash/github actions today, but we may
want to migrate this to a more robust testing framework in the future (ie. 
driving the cmdline tool from a test suite)

For more details look at the .github/workflows/pr-smoke-testing.yaml

## End-to-end integration testing

E2E Integration tests can be run locally with:
    
```bash
make integration-test # or run-integration-test if you have a DB/deployment ready
```

This target calls the `mock-integration-test` and heavier `deployment-integration-test`.

### On deployment E2E testing

We maintain the test files for on-deployment end-to-end tests in the `/tests/e2e/<component>`
directory, ie. `/tests/e2e/agent`, `/tests/e2e/gitops`, etc.

End-to-end tests are used to test the system as a whole. Our stack helps deploy a
complete system with `make deploy` and that can be used for some types of
end-to-end testing, although testing in that way makes it harder to debug in
the event of an error (for example debugging across multiple containers and processes).

Agents are deployed as deployed as VMs via qemu on the host, the server, db
and any other dependencies run in [kind](https://kind.sigs.k8s.io/).

We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

```bash
ADMIN_CLIENT_CONFIG=~/.flightctl/client.yaml # optionally target an specific system
make deployment-integration-test # or run-deployment-integration-test if you have a deployment ready
```

### Mocked E2E testing

We maintain the test files for on-deployment end-to-end tests in the `/tests/mock-e2e/<component>`
directory, ie. `/tests/mock-e2e/agent`, `/tests/mock-e2e/gitops`, etc.

To mininize the complexities of debugging distributed systems when a bug is found,
we provide a test harness in `/test/harness` which help test the system in a more
controlled way, over a single process. The test harness expects a DB server,
and creates an ephemeral server, test database, and agent on a virtual directory,
which can be used to interact with them all, inspect or modify files created by
the agent from go, or debug the three together via dlv from the comfort of your
IDE of choice.

```bash
make mock-integration-test # or run-mock-integration-test if you have a DB ready
```

## Performance and scalability testing

We maintain benchmarks in the '/test/perf/' directory. These are very similar to
the e2e deployment tests. They are used to test the performance of the system under
test. They can be ran against a local deployment `make deploy` or a remote deploymet.

This will employ the device simulator code, or direct API calls, to measure the response
times of the system, it will provide reports on ability of the service to handle load.

Results of those tests are very machine dependent, and should only be used to compare
differences when making architectural changes, or to analyze the performance of an specific
deployment/topology.

TBD: parameters and key results to look for.

```bash
ADMIN_CLIENT_CONFIG=~/.flightctl/client.yaml # optionally target an specific system
make perf-test # or run-perf-test if you have a deployment ready
```

## Update testing

Update testing will performed in CI, and we should start running it after our first release,
to ensure that we can update at least one version of the system without breaking it.

This is an overall idea of how update testing should be performed, but still under definition:
* deploy the previous version of the system
* populate a set of devices/resources
* update the new version of the system
* verify devices/resources are still functional (agents are not updated yet)
* update agents to the newer version
* verify devices/resources are still functional


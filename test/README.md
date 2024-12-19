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
directory, ie. `/test/e2e/agent/`, `/test/e2e/cli/`, etc.

as examples:

* `agent` contains tests for the agent component in interaction with the OS and
          registries: switching an image, rebooting, failure and rollback, etc.

* `gitops` contains tests for the server that verify interaction
           with external git repositories.

* `k8s/secrets` contains for the server that verify interaction with a k8s API
                in terms of secret retrieval.

* `cli` contains tests for the command line interface `flightctl`

E2E tests can be run with our testing harness in `/test/pkg/harness/e2e` which
provides additional functionality on top of `/test/pkg/harness` to interact
with agents on VMs, or connect the server to the local kind k8s cluster.

We use ginkgo/gomega for these tests, as they are more complex and require
more setup and teardown than unit tests.

#### Filtering the e2e test run

You can filter which e2e tests to run by pointing to the e2e directory
using the GO_E2E_DIRS environment variable.

For example, if we wanted to run only the cli tests, we could execute
```
make e2e-test GO_E2E_DIRS=test/e2e/cli
```

or, if we ran e2e-test before and all the necessary artifacts and deployments are
in place, we could speed up furter by using the `run-e2e-test` target.
```
make run-e2e-test GO_E2E_DIRS=test/e2e/cli
```

You can also filter by providing the GINKGO_FOCUS environment variable, which
will filter the tests by the provided string.
````
make e2e-test GINKGO_FOCUS="should create a new project"
````

#### Environment flags

* `FLIGHTCTL_NS` - the namespace where the flightctl is deployed, this is
  used by the scripts to figure out the routes/endpoints.

* `KUBEADMIN_PASS` - the OpenShift kubeadmin (or user with the right to
  authenticate to flightctl) password for the cluster, used sometimes to
  request a token for automatic login.

* `DEBUG_VM_CONSOLE` - if set to `1`, the VM console output will be printed
  to stdout during test execution.

### Local testing side services
For the purpose of providing a local testing environment, we have a set of side services
that run inside the kind cluster to provide a complete testing environment.
Therefore, [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/) and [kind](https://kind.sigs.k8s.io/) must be installed.

#### Local container registry

Running on `${IP}:5000` and `localhost:5000`, exposed via TLS,
we configure the test host to consider that an insecure registry, but we configure
the agents to trust the CA generated by the `test/scripts/create_e2e_certs.sh` script.

For E2E testing we build several agent images that we push into this registry,
that we use to exercise the agent and update through in the tests,
more details can be found here  [Agent Images](./scripts/agent-images/README.md)

#### Local ssh+git server
Running on ${IP}:3222 and localhost:3222, authentication to this
repository can be performed with the `bin/.ssh/id_rsa` key with the `user` user,
the git ssh connection also accepts the `user` password.

This is an example `~/.ssh/config` entry, assuming that flightctl is checked out in `~/flightctl` and
deployed with `make deploy`:

```
Host gitserver
     Hostname localhost
     Port 3222
     IdentityFile ~/flightctl/bin/.ssh/id_rsa
```

Connection via ssh allows three commands:
* create-repo <name> - creates a new git repository with the given name
* delete-repo <name> - deletes the git repository with the given name
* quit/exit - closes the connection

Example:
```bash
$ ssh user@gitserver -p3222
git> create-repo test1
Initialized empty Git repository in /home/user/repos/test1.git/
git> create-repo test2
Initialized empty Git repository in /home/user/repos/test2.git/
git> delete-repo test1
git> delete-repo test2
git> quit
Connection to 192.168.1.10 closed.
```

Repositories can be accessed as:
```bash
git clone user@gitserver:repos/test1.git
```

### Running E2E tests
```bash
make e2e-test
```

### Running E2E with an existing cluster
If you have a cluster already running, you can run the tests with:
```bash
export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password-for-kubeadmin

make in-cluster-e2e-test
```

You can also use `FLIGHTCTL_RPM=release/0.3.0`, `FLIGHTCTL_RPM=devel/0.3.0.rc1-5.20241104145530808450.main.19.ga531984`
or simply `FLIGHTCTL_RPM=release` or `FLIGHTCTL_RPM=devel` to consume an specific version/repository
of the CLI and agent rpm.

I.e. if you wanted to test the cluster along with the 0.3.0 release in
https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl/builds/, you would run:
```bash
export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password-for-kubeadmin
export FLIGHTCTL_RPM=release/0.3.0

make in-cluster-e2e-test
```

If you wanted to test the cluster along with the latest devel build in
https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev/builds/, you could use run:
```bash
export FLIGHTCTL_RPM=devel/0.3.0.rc2-1.20241104145530808450.main.19.ga531984

make in-cluster-e2e-test
```

#### If your host system is not suitable for bootc image builder

* Install vagrant
```bash
sudo dnf install vagrant
```
* Install vagrant-libvirt plugin
```bash
vagrant plugin install vagrant-libvirt
```

* Continue inside vagrant

```bash
cp you-kubeconfig-file ~/.kube/config

cd ~/flightctl

# this may be necessary for vagrant libvirtd to work
sudo systemctl enable --now virtnetworkd

vagrant up --provider libvirt
vagrant ssh

cd /vagrant

export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password-for-kubeadmin
make in-cluster-e2e-test

exit
```

If you are debugging and modifying scripts or tests on the host machine
you can use the `vagrant rsync` or `vagrant rsync-auto` commands to sync
the files between the host and the vagrant machine.

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

# Developer Documentation

## Building

Prerequisites:
* `git`, `make`, and `go` (>= 1.23), `openssl`, `openssl-devel`, `buildah`, `pam-devel`, `podman`, `podman-compose`, `container-selinux` (>= 2.241), `go-rpm-macros` (in case one needs to build RPM's), `python3`, and `python3-pyyaml` (or install PyYAML via pip)

Flightctl agent reports the status of running rootless containers. Ensure the podman socket is enabled:

`systemctl --user enable --now podman.socket`

Checkout the repo and from within the repo run:

```
make build
```

To run unit tests, use `make unit-test`.  This requires installing gotestsum:

`go install gotest.tools/gotestsum@latest`

To run integration tests, use `make integration-test`. This requires Podman (or Docker) - the test
framework uses testcontainers to automatically start ephemeral Postgres, Redis, and Alertmanager
instances. No manual setup is required.

Key options for integration tests:
- `INTEGRATION_PROCS=N` - Number of parallel processes (default: 4)
- `TEST_DIR=./test/integration/store` - Run specific test suite
- `INTEGRATION_GINKGO_FOCUS="pattern"` - Run tests matching pattern

Example: `make integration-test TEST_DIR=./test/integration/agent INTEGRATION_PROCS=1`

To generate API code and mocks, use `make generate`  This requires installing mockgen:

`go install go.uber.org/mock/mockgen@v0.4.0`

### Container image building

Flight Control supports building containers for multiple Enterprise Linux versions using OS-qualified image naming. The build system uses an `OS` variable to specify the target Enterprise Linux version:

```bash
# Build containers for Enterprise Linux 9 (default)
make build-containers OS=el9

# Build containers for Enterprise Linux 10
make build-containers OS=el10
```

**OS-Qualified Image Names:**

All Flight Control service containers use OS-qualified naming with the format `flightctl-{service}-{OS}:latest`:

- **EL9**: `flightctl-api-el9:latest`, `flightctl-worker-el9:latest`, etc.
- **EL10**: `flightctl-api-el10:latest`, `flightctl-worker-el10:latest`, etc.

This naming scheme prevents conflicts when building and deploying containers across different Enterprise Linux versions.

**Available Make Targets:**

- `make build-containers` - Build all service containers (defaults to `OS=el9`)
- `make clean-containers` - Remove containers for the current OS
- `make bundle-containers` - Bundle containers for distribution

**Registry Image Naming:**

Registry images follow the same pattern: `quay.io/flightctl/flightctl-{service}-{OS}:{version}`

## Running

Note: If you are developing with podman on an arm64 system (i.e. M1/M2 Mac) change the postgresql
image with:
```
export PGSQL_IMAGE=registry.redhat.io/rhel9/postgresql-16
podman login registry.redhat.io
```

The service can be deployed locally in kind with the following command:
```
make deploy
```

Note: An update to firewalld may need to be made if the agent is unable to connect to the api instance:

```bash
VIRBR0_SUBNET=`ip a | grep 'virbr0:' -A 2 | tail -1 | awk '{print $2}'`
sudo firewall-cmd --zone=libvirt --add-rich-rule="rule family=\"ipv4\" source address=\"$VIRBR0_SUBNET\" accept" --permanent
sudo firewall-cmd --reload
```

### Deployment using Quadlets

The service can also be deployed using systemd Quadlets (Podman containers managed by systemd):
```
make deploy-quadlets
```

Note it stores its generated CA cert, server cert, and client-bootstrap cert in `$HOME/.flightctl/certs`
and the client configuration in `$HOME/.flightctl/client.yaml`.

Use the `flightctl` CLI to login and then apply, get, or delete resources:

```
bin/flightctl login $(cat ~/.flightctl/client.yaml | grep server | awk '{print $2}') --web --certificate-authority ~/.flightctl/certs/ca.crt
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
bin/flightctl get fleet fleet1 fleet2  # Get multiple specific resources
```

Use an agent VM to test a device interaction, an image is automatically created from
hack/Containerfile.local and a qcow2 image is derived in output/qcow2/disk.qcow2, currently
this only works on a Linux host.

You can deploy a DB container of different sizes using a DB_VERSION variable for make command:
* e2e (default) - minimal footprint for e2e testing
* small-1k - recommended setting for a demo environment 1000 devices max
* medium-10k - recommended setting for a demo environment 10k devices max

```
# will create the cluster, and the agent config files in bin/agent which will be embedded in the image
# this one will create a default `e2e` DB container
make deploy
# to create a small DB container use
# make deploy DB_VERSION=small
make agent-vm
```

The VM uses user-session libvirt (`qemu:///session`) with QEMU user-networking, so no root is required.
SSH is available on `127.0.0.1:2222` (password: `user`).

The `agent-vm` target accepts the following parameters:

- `VMNAME`: the name of the VM to create (default: `flightctl-device-default`)
- `VMRAM`: the amount of memory in MiB to allocate to the VM (default: `2048`)
- `VMSSHPORT`: the host port forwarded to the VM's SSH (default: `2222`)

It is possible to create multiple VMs with different names and ports:

```
make agent-vm VMNAME=flightctl-device-1 VMSSHPORT=2223
make agent-vm VMNAME=flightctl-device-2 VMSSHPORT=2224
make agent-vm VMNAME=flightctl-device-3 VMSSHPORT=2225
```

Attach to the serial console of a running VM (exit with Ctrl+]):

```
make agent-vm-console VMNAME=flightctl-device-1
```

Or connect via SSH (password: `user`):

```
ssh -p 2223 -o StrictHostKeyChecking=no user@127.0.0.1
```

If you created individual devices you need to clean them one by one:

```
make clean-agent-vm VMNAME=flightctl-device-1
make clean-agent-vm VMNAME=flightctl-device-2
make clean-agent-vm VMNAME=flightctl-device-3
```

To quickly create agent instances for testing/development in a containerized environment. This is particularly useful for testing lightweight agent deployments without setting up VMs.

```
make agent-container
make clean-agent-container
```

Use the **[devicesimulator](devicesimulator.md)** to simulate load from devices

```
bin/devicesimulator --count=100
```

## Backup and restore

For backup and restore procedures applicable to development deployments, see [Backup and Restore](../user/installing/backup-restore.md). Development deployments using kind or quadlets can use the same `flightctl-backup` and `flightctl-restore` commands documented for production environments.

## Metrics

The observability stack (Prometheus) is managed by **testcontainers** in [test/e2e/infra/](../../test/e2e/infra/) and starts automatically when you run E2E tests.

To use the Prometheus UI, run the E2E test suite (e.g. `make e2e-test` or `make in-cluster-e2e-test`). While tests run, the stack is up and the Prometheus web UI is typically accessible at `http://localhost:9090` (see `test/e2e/infra/auxiliary/` for details).

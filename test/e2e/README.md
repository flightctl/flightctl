# E2E tests – usage

How to run end-to-end tests, which environment variables to set, and how to manage aux (auxiliary) services. For concepts and guidelines on writing tests, the harness, and aux services, see [GUIDELINES.md](GUIDELINES.md).

## Workflow

You **deploy FlightCtl wherever you want** using the project’s deployment options: **Helm** for Kubernetes or OpenShift (see [Installing on Kubernetes/OpenShift](../../docs/user/installing/installing-service-on-kubernetes.md)), or **Quadlets** (systemd + Podman) for Linux hosts (see [deploy/AGENTS.md](../../deploy/AGENTS.md)). Then you **prepare** (e2e certs and aux services) and **run** the tests against that deployment.

- **Prepare:** `make prepare-e2e-test`, then `make start-aux` if the tests need registry/git/prometheus.
- **Run:** `make run-e2e-test` (set env vars as needed for your deployment; see below).

If you do not have a deployment yet, you can use the **local deployment options** (same stack, local only):

- **Kubernetes (kind):** `make deploy` — creates a kind cluster and deploys FlightCtl there. Then prepare and run as above.
- **Quadlet:** `make deploy-quadlets` — deploys FlightCtl via Quadlets on the current host. Then set [Quadlet environment](#quadlet-environment-running-e2e-against-quadlet) vars and run.

The tests only assume that FlightCtl is already running and reachable (cluster or Quadlet).

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/) and [kind](https://kind.sigs.k8s.io/) (if you use `make deploy`)
- Podman
- Build tools (for agent images, qcow2)
- Optional: libvirt for agent VMs; see suite-specific READMEs (e.g. [quadlets/README.md](quadlets/README.md), [tpm/README.md](tpm/README.md), [rollout/README.md](rollout/README.md))

## Prepare and run (targets)

**Prepare (once or when resetting):**

| Target | Description |
|--------|-------------|
| `make prepare-e2e-test` | Create e2e certs and SSH keys under `bin/e2e-certs/` and `bin/.ssh/`. Run once before first e2e or before `start-aux`. |
| `make start-aux` | Start all aux containers (registry, git-server, prometheus). Requires `make prepare-e2e-test` first. |

**Run (execute tests):**

| Target | Description |
|--------|-------------|
| `make run-e2e-test` | Run the e2e test suite against your deployment. Set env vars as needed (see [Environment variables](#environment-variables)). |

**Local deployment (optional):**

| Target | Description |
|--------|-------------|
| `make deploy` | Deploy a kind cluster and FlightCtl. Use when you want a local Kubernetes deployment; then prepare and run. |
| `make deploy-quadlets` | Deploy FlightCtl via Quadlets on the current host. Use when you want a local Quadlet deployment; then set Quadlet env vars and run. |
| `make e2e-test` | All-in-one: deploy (kind), prepare, build agent images/qcow2, and run. Convenience when starting from a clean state with no existing deployment. |

**Aux (start/stop individual services):**

| Target | Description |
|--------|-------------|
| `make stop-aux` | Stop all aux containers. |
| `make clean-aux` | Remove aux containers and their volumes for a fresh run. |
| `make start-registry`, `make start-git-server`, `make start-prometheus`, `make start-tracing` | Start one aux service. |
| `make stop-registry`, `make stop-git-server`, `make stop-prometheus`, `make stop-tracing` | Stop one aux service. |

## Environment variables

### Cluster and auth

| Variable | When to set | Description |
|----------|-------------|-------------|
| `FLIGHTCTL_NS` | Override namespace | Namespace where FlightCtl is deployed. E2E infra usually discovers it from cluster labels; set only when you need to override (e.g. login flow, CI). |
| `KUBEADMIN_PASS` | OpenShift / existing cluster | Password for kubeadmin (or user with rights to authenticate to FlightCtl). Used to request tokens for automatic login. |
| `KUBECONFIG` | Non-default kubeconfig | Path to kubeconfig. Defaults to `~/.kube/config`. |
| `E2E_ENVIRONMENT` | Optional | Force environment type: `k8s`, `ocp`, or `quadlet`. If unset, inferred from cluster context. |
| `E2E_AUX_HOST` | Two-NIC test VM on OCP | VM IP on the OCP network so registry/git/prometheus are reachable by the cluster. See [OpenShift with shared network](#openshift-with-shared-network-qe). |

### Filtering and behaviour

| Variable | Description |
|----------|-------------|
| `GO_E2E_DIRS` | Restrict to specific e2e packages. Example: `GO_E2E_DIRS=test/e2e/cli`. |
| `GINKGO_FOCUS` | Run only specs whose description matches this string. Example: `GINKGO_FOCUS="should create a new project"`. |
| `GINKGO_LABEL_FILTER` | Run only specs with the given label(s). In CI, `sanity` is often used. Example: `GINKGO_LABEL_FILTER="sanity"`. |
| `GINKGO_PROCS` | Number of parallel test processes. |
| `DEBUG_VM_CONSOLE` | Set to `1` to print VM console output to stdout during test execution. |

### Builds and versions

| Variable | Description |
|----------|-------------|
| `FLIGHTCTL_RPM` | Use a specific CLI/agent RPM from Copr. Examples: `release/0.3.0`, `devel`, `devel/0.3.0.rc2-1.20241104145530808450.main.19.ga531984`. |
| `BREW_BUILD_URL` | Use RPMs from Red Hat Brew (URL to the Brew task page). Agent image and CLI are built from these RPMs. |

### Quadlet environment (running e2e against Quadlet)

When running e2e against a Quadlet deployment (e.g. after `make deploy-quadlets` on this host or a remote Quadlet host), set:

| Variable | Description |
|----------|-------------|
| `E2E_ENVIRONMENT` | Set to `quadlet` so infra uses Quadlet providers. |
| `E2E_SSH_HOST` | SSH host of the Quadlet device (e.g. `localhost` when using `make deploy-quadlets` on this host). |
| `E2E_SSH_USER` | SSH username to run commands on the device. |
| `E2E_SSH_KEY_PATH` | Path to SSH private key for `E2E_SSH_USER`. Defaults to `~/.ssh/id_rsa` if unset. |
| `E2E_SSH_PASSWORD` | SSH password (alternative to key). Used when `E2E_SSH_KEY_PATH` is not set; requires `sshpass` on the test host. |
| `E2E_USE_SUDO` | Use sudo for systemctl/podman on the device. Default: `true` for Quadlet. |
| `E2E_CONFIG_DIR` | FlightCtl config directory on the device. Default: `/etc/flightctl`. |
| `E2E_API_ENDPOINT` | FlightCtl API URL (e.g. `https://<host>:3443`). Inferred from host if unset. |
| `E2E_PAM_USER` | PAM user for `flightctl login` (Quadlet/standalone API). Default: `admin`. |
| `E2E_PAM_PASSWORD` | PAM password for login. |
| `E2E_DEFAULT_PAM_PASSWORD` | Fallback PAM password if `E2E_PAM_PASSWORD` is unset (e.g. test default). |

### Other

| Variable | Description |
|----------|-------------|
| `REGISTRY_ENDPOINT` | Set by e2e infra (e.g. from aux) for deploy/scripts. Usually not set manually. |

## Filtering the e2e run

```bash
# Only the CLI tests
make run-e2e-test GO_E2E_DIRS=test/e2e/cli

# By spec description
make run-e2e-test GINKGO_FOCUS="should create a new project"

# By Ginkgo label (e.g. sanity in CI)
make run-e2e-test GINKGO_LABEL_FILTER="sanity"
```

## Aux (auxiliary) services

Aux services are shared testcontainers used by e2e: **registry** (TLS on port 5000), **git server** (SSH on port 2222), **Prometheus** (port 9090), and optionally **tracing** (Jaeger). They run the same for kind and Quadlet; the e2e framework starts them when suites call `auxiliary.Get(ctx)` (or you can start them manually for debugging).

- **Start all:** `make start-aux` (after `make prepare-e2e-test`).
- **Stop all:** `make stop-aux`.
- **Clean for fresh run:** `make clean-aux` (removes containers and volumes).

Registry is at `${IP}:5000` / `localhost:5000` (TLS). The test host is configured to treat it as an insecure registry; agents use the CA from `test/scripts/create_e2e_certs.sh`. See [Agent Images](../scripts/agent-images/README.md) for how agent images are built and pushed.

Git server: SSH on port 2222, user `user`, key `bin/.ssh/id_rsa`. Example `~/.ssh/config` (with flightctl in `~/flightctl`):

```
Host gitserver
     Hostname localhost
     Port 3222
     IdentityFile ~/flightctl/bin/.ssh/id_rsa
```

Connection allows: `create-repo <name>`, `delete-repo <name>`, `quit`/`exit`. Clone: `git clone user@gitserver:repos/<name>.git`.

## Example: running against an existing cluster

When your FlightCtl deployment is on a cluster you already have (e.g. OpenShift), prepare once then run:

```bash
make prepare-e2e-test
make start-aux   # if suites need registry/git/prometheus

export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password-for-kubeadmin

make run-e2e-test
```

With a specific CLI/agent RPM:

```bash
export FLIGHTCTL_RPM=release/0.3.0
make run-e2e-test
```

With a Brew build:

```bash
export BREW_BUILD_URL=brew-registry-build-url
make run-e2e-test
```

## OpenShift namespace

E2E infra discovers the FlightCtl namespace from the cluster (pods with labels `flightctl.service=flightctl-api` and `flightctl.service=flightctl-worker`). Set **`FLIGHTCTL_NS`** only when you need to override (e.g. for `kubectl create token` or CI).

Optional: set `E2E_ENVIRONMENT=ocp` so the environment is not auto-detected.

## OpenShift with shared network (QE)

When the test VM and the OpenShift cluster share a network (e.g. same libvirt network), the cluster can reach aux services on the VM. No in-cluster registry is required.

- Set `E2E_ENVIRONMENT=ocp` (or rely on context detection). Start aux as usual; suites that need it call `auxiliary.Get()` and pass registry/git data from the result into the harness and scripts.
- If the test VM has **two NICs**, set `E2E_AUX_HOST` to the VM’s IP on the OCP network so registry/git/prometheus are advertised on the interface the cluster can reach:

```bash
export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password-for-kubeadmin
export E2E_AUX_HOST=192.168.122.10   # VM IP on the OCP network

make run-e2e-test
```

## OCP test VM (imagebuilder / non-suitable host)

If your host is not suitable for the bootc image builder:

1. Create a test VM: `KUBECONFIG_PATH=/path/to/your/kubeconfig make deploy-e2e-ocp-test-vm`. Note the SSH command.
2. SSH into the VM and run e2e from inside (see test/README.md for full flow with `oc login`, then `make prepare-e2e-test` and `make run-e2e-test`).

Optional: `VM_DISK_SIZE_INC` to increase disk size.

## More information

- **Writing tests, harness, aux:** [GUIDELINES.md](GUIDELINES.md)
- **Test strategy and integration/unit:** [../README.md](../README.md)
- **Suite-specific requirements:** [quadlets/README.md](quadlets/README.md), [tpm/README.md](tpm/README.md), [rollout/README.md](rollout/README.md)

# Package-mode development notes

Package-mode devices run the Flight Control agent on hosts that use traditional
package management (dnf/yum) without `bootc` or `rpm-ostree` image management.
This page summarizes RPM packaging and package-mode e2e for contributors.

For user-facing install and mixed-fleet behavior, see
[Installing and configuring the Flight Control Agent](../user/installing/installing-agent.md)
and
[Mixed image-mode and package-mode fleets](../user/using/managing-fleets.md#mixed-image-mode-and-package-mode-fleets).

## RPM subpackages

The agent RPM split is defined in [`packaging/rpm/flightctl.spec`](../../packaging/rpm/flightctl.spec):

| Package | Role | Dependencies |
| ------- | ---- | ------------ |
| `flightctl-agent` | Agent binary, systemd unit, SELinux requirement, core files | `Recommends: flightctl-greenboot` |
| `flightctl-greenboot` | Greenboot health checks, bootc timer masking, greenboot configuration | `Requires: greenboot` and matching `flightctl-agent` |

Image-mode installs use the default weak-dependency behavior and pull in
`flightctl-greenboot`. Package-mode installs use
`--setopt=install_weak_deps=False` so greenboot is not installed. See the
[user install docs](../user/installing/installing-agent.md#installing-the-agent-rpm).

## Package-mode e2e

Package-mode e2e uses the `package` **agent-image variant** (bootc base with
`bootc`/`rpm-ostree` removed from PATH so `DetectMode` reports package mode)
and a dedicated Ginkgo suite under `test/e2e/package_mode`. Tests run the
agent in a **testcontainer** (systemd as init, nested Podman for apps), not a
package-mode QCOW2 VM.

### Build the package variant

```bash
make e2e-agent-images
```

This builds the bootc base, all default variants (including `package`), and
the agent bundle for `AGENT_OS_ID` (default `cs9-bootc`). The harness image
reference is:

`quay.io/flightctl/flightctl-device:package`

(`DeviceTags.Package` via `NewDeviceImageReference`, same tagging scheme as
`v2`/`v3`/…).

CI loads that tag from the agent bundle into local Docker/Podman storage
before e2e. For OS flavors, tagging, and bundling, see
[test/scripts/agent-images/README.md](../../test/scripts/agent-images/README.md).

### Start a package-mode agent container locally

For a quick local container (host network, no e2e harness setup):

```bash
make agent-container
make clean-agent-container
```

Override the image with `PACKAGE_MODE_AGENT_IMAGE` if needed. See
[`deploy/agent-vm.mk`](../../deploy/agent-vm.mk).

### Run the package-mode suite

```bash
make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/package_mode
```

Prerequisites:

* Agent config under `bin/agent/etc/flightctl` (from a normal deploy / e2e setup)
* The `package` OCI image loaded locally (from `make e2e-agent-images` or the CI bundle)

The harness helper `StartPackageModeAgent` (in
`test/harness/e2e/package_mode_agent.go`) starts a privileged testcontainer
with `/sbin/init`, mounts agent config and certs, configures registry access,
and waits for `flightctl-agent` to become active.

### What the suite covers

* Enrollment reports `status.capabilities.osMode=package`
* Config and a Podman application on a fleet **without** `spec.os.image`
* When the fleet adds `spec.os.image`, the package-mode device stays
  `OutOfDate` and does not advance the committed config version

Mixed package-mode + image-mode **VM** scenarios remain skipped until
image-mode VM infrastructure is available for that pairing. See the
Package-mode E2E section of [`test/AGENTS.md`](../../test/AGENTS.md) for CI
wiring and harness notes.

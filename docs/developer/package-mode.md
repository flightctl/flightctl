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

Package-mode e2e uses a `cs9-regular` **OCI** agent image and a dedicated
Ginkgo suite under `test/e2e/package_mode`. Tests run the agent in a
**testcontainer** (systemd as init, nested Podman for apps), not a
package-mode QCOW2 VM.

### Build the cs9-regular image

```bash
BUILD_TYPE=regular AGENT_OS_ID=cs9-regular SKIP_QCOW_BUILD=true make e2e-agent-images
```

With `SKIP_QCOW_BUILD=true` (the CI default for package-mode), the path builds
the package-mode OCI base and bundle only. Do not rely on a package-mode
QCOW2 disk for e2e.

CI builds `cs9-regular` with `build_type: regular`, `upload_bundle: true`, and
`skip_qcow_build: true`, then loads `agent-images-bundle-cs9-regular.tar` into
local container storage before e2e.

Image reference used by the harness:

`quay.io/flightctl/flightctl-device:base-cs9-regular`

For OS flavors, tagging, and bundling, see
[test/scripts/agent-images/README.md](../../test/scripts/agent-images/README.md).

### Run the package-mode suite

```bash
make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/package_mode
```

Prerequisites:

* Agent config under `bin/agent/etc/flightctl` (from a normal deploy / e2e setup)
* The `cs9-regular` OCI image loaded locally (from the build or CI bundle)

The harness helper `StartPackageModeAgent` (in
`test/harness/e2e/package_mode_agent.go`) starts a privileged testcontainer
with `/sbin/init`, mounts agent config and certs, and waits for
`flightctl-agent` to become active.

### What the suite covers

* Enrollment reports `status.capabilities.osMode=package`
* Config and a Podman application on a fleet **without** `spec.os.image`
* When the fleet adds `spec.os.image`, the package-mode device stays
  `OutOfDate` and does not advance the committed config version

Mixed package-mode + image-mode **VM** scenarios remain skipped until
image-mode VM infrastructure is available for that pairing. See the
Package-mode E2E section of [`test/AGENTS.md`](../../test/AGENTS.md) for CI
wiring and harness notes.

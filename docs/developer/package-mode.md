# Package-mode development notes

Package-mode devices run the Flight Control agent on hosts that use traditional
package management (dnf/yum) without `bootc` or `rpm-ostree` image management.
This page summarizes RPM packaging and e2e image builds for contributors.

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

## Package-mode e2e agent images

E2E agent image builds live under `test/scripts/agent-images/`. Package-mode
coverage uses the `cs9-regular` flavor:

```bash
BUILD_TYPE=regular AGENT_OS_ID=cs9-regular make e2e-agent-images
```

That path builds:

* An OCI base image from `containerfiles/cs9-regular/Containerfile`
* A qcow2 disk image via `scripts/qcow2_regular.sh` (cloud image +
  `virt-customize`), not `bootc-image-builder`

OS-update e2e scenarios that depend on image-based OS switching do not apply
to package-mode images. Prefer config and application management coverage for
`cs9-regular`.

For flavors, tagging, variants, and CI wiring details, see
[test/scripts/agent-images/README.md](../../test/scripts/agent-images/README.md).

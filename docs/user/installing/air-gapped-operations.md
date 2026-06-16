# Operating Flight Control in an air-gapped environment

This document covers day-2 operations for Flight Control deployments with no internet
access: managing fleets, delivering configuration updates, and applying OS image updates
to devices using a local registry.

For installation procedures see:

- [Installing the Flight Control service offline on Linux](installing-service-on-linux-offline.md)
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md)

## Fleet management in an air-gapped environment

Fleet management operations — applying configuration, monitoring device status, rolling
out policies — work identically in air-gapped deployments. No external DNS, internet
access, or connection to public registries is required at runtime.

### How it works

- Devices communicate with the Flight Control API server exclusively over the internal
  network using mTLS. All traffic stays within the air-gapped environment.
- The agent polls the API server at a configured interval to check for new configuration
  or OS image targets. No inbound connection to the device is needed.
- Configuration updates, fleet policy changes, and rollout operations are delivered
  through the standard Flight Control workflow — the air-gap is transparent to the
  operator.

### Verifying device connectivity

Confirm that enrolled devices are reporting status to the API server:

```bash
flightctl get devices
```

A device with `Online` status is actively communicating with the server over the
internal network. A device showing `Unknown` or `Offline` may have a network path
problem between the device and the server — verify that the device can reach the
server's FQDN on port `7443`.

### Applying configuration updates

Configuration updates are delivered through the standard fleet workflow. Create or
update a fleet configuration:

```bash
flightctl apply -f my-fleet-config.yaml
```

The agent picks up the change on its next poll cycle and applies it locally. No
internet access is involved — only the internal API server connection.

To check rollout progress:

```bash
flightctl get fleet <fleet-name>
flightctl get devices -l fleet=<fleet-name>
```

For full fleet management documentation see
[Managing Fleets](../using/managing-fleets.md).

---

## Image updates using a local registry

In an air-gapped environment, container images must be staged in a local registry
before devices can pull them. This applies to both OS (bootc) image updates and
application workload images managed by the agent. The workflow is the same in both
cases: mirror the image to the local registry, then target it in the fleet or device
configuration.

> [!NOTE]
> For application workload images (non-OS containers), see
> [Making container images available for managed workloads](installing-agent-offline.md#part-2-making-container-images-available-for-managed-workloads)
> for the device-side setup including local registry and direct image loading.

### OS image update workflow

### Prerequisites

- A local container registry reachable from all devices on the internal network
- The new OS image mirrored into that registry (see below)
- `flightctl-cli` configured to reach the internal API server

### Step 1: Mirror the new OS image to the local registry

On a connected prep machine, pull the new OS image and push it to your internal
registry using `skopeo`:

```bash
# Mirror from the upstream source to the local registry
skopeo copy \
    docker://<source-registry>/<org>/<image>:<tag> \
    docker://<local-registry-host>:<port>/<org>/<image>:<tag>
```

For images built with the Flight Control image builder, export the built image
from the image builder service and push it to the local registry:

```bash
skopeo copy \
    docker://<imagebuilder-registry>/<image>:<tag> \
    docker://<local-registry-host>:<port>/<image>:<tag>
```

Transfer via portable media if the prep machine cannot reach the local registry
directly — see [Packaging artifacts for portable media](offline-portable-media.md).

### Step 2: Configure registry authentication on devices (if required)

If the local registry requires authentication, configure devices to authenticate
using image pull secrets. See
[Using Image Pull Secrets](../using/managing-devices.md#using-image-pull-secrets)
for the full procedure.

### Step 3: Target the new OS image in the fleet configuration

Update the fleet's `osImage` reference to point at the new image in the local
registry:

```yaml
apiVersion: v1alpha1
kind: Fleet
metadata:
  name: my-fleet
spec:
  template:
    spec:
      os:
        image: <local-registry-host>:<port>/<image>:<tag>
```

Apply the updated fleet configuration:

```bash
flightctl apply -f my-fleet.yaml
```

### Step 4: Monitor the rollout

The agent picks up the new image target on its next poll cycle, pulls the image from
the local registry, and applies the update using `bootc`. Monitor rollout progress:

```bash
flightctl get fleet my-fleet
flightctl get devices -l fleet=my-fleet
```

Individual device update status is visible in the device's `status.updated` field:

```bash
flightctl get device <device-name> -o yaml | grep -A5 "updated:"
```

### Step 5: Verify the update completed

After the device reboots into the new image, confirm the update succeeded by
checking the device status from the Flight Control server:

```bash
flightctl get device <device-name> -o yaml | grep -A10 "os:"
```

The `status.os.image` field will show the currently running image once the device
has applied the update and reported back.

> [!NOTE]
> OS updates use the `bootc` container storage transport. The device pulls the image
> directly from the local registry over the internal network — no external registry
> access or internet connectivity is required at any point in the update process.

---

## Next steps

- [Managing Fleets](../using/managing-fleets.md) — full fleet management reference
- [Managing Image Builds](../using/managing-image-builds.md) — building device OS
  images with the Flight Control image builder
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md) —
  agent installation and enrollment
- [Packaging artifacts for portable media](offline-portable-media.md) — transferring
  OS images and other artifacts to air-gapped environments

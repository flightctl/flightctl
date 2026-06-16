# Air-gapped Flight Control Installation Guide

This guide provides an end-to-end overview of installing and operating Flight Control
in environments with no internet access. It covers both RHEL (quadlet/systemd) and
OpenShift (Helm) deployment paths, with decision guidance to help you choose the right
approach for your environment.

## Table of contents

- [Choosing your installation path](#choosing-your-installation-path)
- [Common prerequisites](#common-prerequisites)
- [Quick-start: RHEL installation](#quick-start-rhel-installation)
- [Quick-start: OpenShift installation](#quick-start-openshift-installation)
- [Enrolling and managing devices](#enrolling-and-managing-devices)
- [Troubleshooting](#troubleshooting)
- [Reference: all air-gap documentation](#reference-all-air-gap-documentation)

---

## Choosing your installation path

```text
Do you have an OpenShift or Kubernetes cluster?
│
├── YES → Use the OpenShift/Helm installation path
│         Choose a variant when running flightctl-mirror-images:
│           community-el9/el10  — quay.io images, no entitlement required
│           rhem-el9/rhem-el10  — registry.redhat.io images, requires entitlement
│
└── NO  → Use the Linux quadlet installation path
          (systemd + podman, RPM-based)
          Works on any Linux with skopeo, createrepo_c, and dnf available
          (RHEL, CentOS Stream, Fedora, etc.)
```

### Linux quadlet vs OpenShift at a glance

| | Linux quadlet | OpenShift (Helm) |
|---|---|---|
| **Runtime** | systemd + podman | Kubernetes pods |
| **Package format** | RPM (`flightctl-services`) | Helm chart |
| **Image redirection** | `registries.conf` | `ImageTagMirrorSet` |
| **Bundle includes** | Images + RPMs | Images only |
| **Typical bundle size** | 5–10 GB | 5–15 GB |
| **Prerequisites on target** | podman, skopeo | OCP cluster, internal registry |

---

## Common prerequisites

Regardless of which path you choose, the prep machine requires:

- Any Linux with `dnf` available (RHEL, CentOS Stream, Fedora, etc.)
- `skopeo` installed (`sudo dnf install -y skopeo`)
- `createrepo_c` installed (`sudo dnf install -y createrepo_c`) — required for
  `--rpm-createrepo` and `--agent-only` bundles
- The `flightctl-mirror-images` binary — installed via `flightctl-cli` RPM, or built
  from the flightctl repository (`make build-mirror-images`)
- Sufficient disk space (10–20 GB depending on variant)
- A transfer method to move artifacts to the air-gapped environment

---

## Quick-start: RHEL installation

Complete checklist for installing Flight Control on a RHEL machine with no internet
access.

### On the prep machine

- [ ] Install prerequisites: `sudo dnf install -y skopeo createrepo_c`
- [ ] Install or build `flightctl-mirror-images`
- [ ] Determine the target RPM version:

  ```bash
  dnf list --showduplicates flightctl-services | tail -5

  ```

- [ ] Create the bundle:

  ```bash
  flightctl-mirror-images \
      --variant community-el9 \
      --bundle ~/flightctl-bundle.tar.gz \
      --bundle-rpms \
      --rpm-createrepo \
      --rpm-exclude flightctl-agent \
      --tag-override <version>

  ```

- [ ] Transfer the bundle to the target machine:

  ```bash
  scp ~/flightctl-bundle.tar.gz <user>@<target>:~/

  ```

### On the target RHEL machine

- [ ] Install prerequisites (while connected): `sudo dnf install -y skopeo podman containernetworking-plugins`
- [ ] Extract the bundle:

  ```bash
  mkdir ~/flightctl-bundle
  tar -xzf ~/flightctl-bundle.tar.gz -C ~/flightctl-bundle

  ```

- [ ] Bootstrap the local registry from the bundle:

  ```bash
  skopeo copy \
      "dir:$HOME/flightctl-bundle/images/library/registry:2" \
      "containers-storage:docker.io/library/registry:2"
  mkdir -p ~/registry-data
  podman run -d --name local-registry --network=host \
      --security-opt label=disable \
      -v ~/registry-data:/var/lib/registry \
      --restart=always docker.io/library/registry:2

  ```

- [ ] Verify registry is ready: `curl http://localhost:5000/v2/`
- [ ] Import all images (wait for completion): `cd ~/flightctl-bundle && ./import.sh`
- [ ] Verify all images imported: `curl http://localhost:5000/v2/_catalog`
- [ ] Install the RPMs: `./install-rpms.sh`
- [ ] Configure registry redirection in `/etc/containers/registries.conf`:

  ```toml
  [[registry]]
  prefix = "quay.io"
  location = "localhost:5000"
  insecure = true

  [[registry]]
  prefix = "docker.io"
  location = "localhost:5000"
  insecure = true

  [[registry]]
  prefix = "registry.access.redhat.com"
  location = "localhost:5000"
  insecure = true

  ```

- [ ] Create `/etc/flightctl/service-config.yaml`:

  ```yaml
  global:
    baseDomain: <your-server-fqdn>
    generateCertificates: builtin
    auth:
      type: oidc

  ```

- [ ] Start services: `sudo systemctl enable --now flightctl.target`
- [ ] Verify all units running: `sudo systemctl list-units flightctl-*.service`
- [ ] Confirm API is reachable:

  ```bash
  curl -k https://<baseDomain>:3443/api/v1/fleets

  ```

**Full instructions:** [Installing the Flight Control service offline on Linux](installing-service-on-linux-offline.md)

---

## Quick-start: OpenShift installation

Complete checklist for installing Flight Control on a disconnected OpenShift cluster.

### On the prep machine

- [ ] Install prerequisites: `sudo dnf install -y skopeo` and `helm` CLI
- [ ] Install or build `flightctl-mirror-images`
- [ ] For `rhem-*` variants: `podman login registry.redhat.io`
- [ ] Mirror images to your internal registry:

  ```bash
  # Option A: direct push
  flightctl-mirror-images \
      --variant rhem-el9 \
      --dest-registry <mirror-registry>:<port> \
      --execute \
      --tag-override <version>

  # Option B: bundle then push
  flightctl-mirror-images \
      --variant rhem-el9 \
      --bundle ~/flightctl-bundle.tar.gz \
      --tag-override <version>

  ```

- [ ] Download the Helm chart:

  ```bash
  helm pull oci://quay.io/flightctl/charts/flightctl --version <version>

  ```

- [ ] Transfer bundle and chart to the disconnected environment

### Inside the disconnected environment

- [ ] Push images to internal mirror registry (if using bundle):

  ```bash
  cd ~/flightctl-bundle && ./import.sh --registry <mirror-registry>:<port>

  ```

- [ ] Apply `ImageTagMirrorSet` for your variant (see
  [Step 3: Configure image mirroring on OpenShift](installing-service-on-openshift-disconnected.md#step-3-configure-image-mirroring-on-openshift)):

  ```bash
  oc apply -f flightctl-mirrors.yaml
  oc wait nodes --all --for=condition=Ready --timeout=30m

  ```

- [ ] Create image pull secret (Red Hat variants only)
- [ ] Install via Helm:

  ```bash
  helm upgrade --install --namespace flightctl --create-namespace \
      flightctl ./flightctl-<version>.tgz --values my-values.yaml

  ```

- [ ] Verify all pods running: `oc get pods -n flightctl`
- [ ] Confirm API is reachable: `curl -k https://<baseDomain>/api/v1/fleets`

**Full instructions:** [Installing Flight Control in a Disconnected OpenShift Cluster](installing-service-on-openshift-disconnected.md)

---

## Enrolling and managing devices

Once the Flight Control service is running, device enrollment and fleet management
operate identically to connected deployments — the only requirement is that devices
can reach the Flight Control API server on the internal network.

### Device enrollment checklist

- [ ] Install `flightctl-agent` on the device (see
  [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md))
- [ ] Distribute CA certificate and enrollment credentials to the device
- [ ] Configure `/etc/flightctl/config.yaml` with the internal server FQDN
- [ ] Start the agent: `sudo systemctl enable --now flightctl-agent`
- [ ] Approve the enrollment request: `flightctl approve enrollmentrequest/<name>`
- [ ] Verify device appears: `flightctl get devices`

### OS image updates

To deliver OS image updates to devices in an air-gapped environment, stage the new
image in the local registry and update the fleet's `os.image` reference. See
[Air-gapped fleet operations and OS image updates](air-gapped-operations.md) for the
full workflow.

---

## Troubleshooting

### Image pull fails on the target machine (RHEL)

**Symptom:** A quadlet service fails to start with `manifest unknown` or `image not found`.

1. Verify the image is in the local registry:

   ```bash
   curl http://localhost:5000/v2/_catalog
   curl http://localhost:5000/v2/<image-path>/tags/list
   ```

2. Verify `registries.conf` has the correct mirror prefix for the image's source registry.
3. Check the image tag — the tag in the local registry must match what the quadlet references.
   Rebuild the bundle with `--tag-override <version>` matching the installed RPM version.

### Image pull fails on OpenShift

**Symptom:** Pods remain in `ImagePullBackOff`.

1. Verify the `MachineConfig` rollout completed: `oc get mcp`
2. Verify the image is in the mirror registry.
3. Check the ITMS source prefix covers the image's registry.

See [ITMS troubleshooting](installing-service-on-openshift-disconnected.md#troubleshooting-common-itms-issues)
for detailed steps.

### RPM install fails with dependency conflict

**Symptom:** `install-rpms.sh` fails with version conflict on system packages (e.g. `librepo`).

This occurs when the bundle was built on a newer RHEL minor version than the target.
The `--nobest` flag in `install-rpms.sh` handles most cases automatically. If the
install still fails, rebuild the bundle on a machine matching the target's RHEL
minor version.

### Device fails to enroll

**Symptom:** Agent logs show connection errors or certificate errors.

1. Verify the device can reach the server FQDN on port `7443`: `curl -k https://<server>:7443/`
2. Verify the CA certificate on the device matches the server's CA.
3. Check the agent logs: `journalctl -u flightctl-agent --no-pager | tail -30`

---

## Reference: all air-gap documentation

### Artifact preparation

- [flightctl-mirror-images tool](../../../scripts/air-gap/README.md) — complete flag reference
- [Setting up a local RPM repository](offline-rpm-repository.md) — `dnf reposync` and local repo setup
- [Packaging artifacts for portable media](offline-portable-media.md) — USB, tar, checksums, size estimates

### RHEL installation

- [Installing the Flight Control service offline on Linux](installing-service-on-linux-offline.md)
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md)

### OpenShift installation

- [Installing Flight Control in a Disconnected OpenShift Cluster](installing-service-on-openshift-disconnected.md)
- [Image Builder configuration for air-gapped environments](configuring-imagebuilder.md#end-to-end-disconnected-image-build-walkthrough)

### Day-2 operations

- [Air-gapped fleet operations and OS image updates](air-gapped-operations.md)
- [Deploying Observability Offline (Linux)](deploying-observability-linux.md#air-gapped-installation)

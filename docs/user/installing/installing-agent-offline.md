# Installing the Flight Control agent offline on RHEL

This document describes how to install `flightctl-agent` and `flightctl-cli` on a RHEL
machine that has no internet access. The procedure uses a connected prep machine to
download the required packages, transfers them using portable media, and installs them
locally on the target.

The agent itself does not require container images to run. However, if the agent manages
containerized workloads on the device, those images must also be made available offline.
Both scenarios are covered here.

## Prerequisites

- A connected RHEL 9 or RHEL 10 prep machine running the same major RHEL version as
  the target
- An air-gapped target RHEL machine
- A transfer method — see [Packaging artifacts for portable media](offline-portable-media.md)
- `sudo` access on both machines

## Part 1: Installing the agent and CLI

### Step 1: Download the packages on the prep machine

Add the FlightCtl repository on the prep machine:

```bash
sudo dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
```

Download `flightctl-agent`, `flightctl-cli`, and the system packages that the
agent's OS image requires (`open-vm-tools`, `ignition`, `afterburn`, `cloud-init`):

```bash
mkdir -p ~/flightctl-rpms
dnf download --resolve --alldeps --destdir ~/flightctl-rpms \
    flightctl-agent flightctl-cli \
    open-vm-tools ignition afterburn cloud-init
```

> [!NOTE]
> `open-vm-tools`, `ignition`, `afterburn`, and `cloud-init` are standard RHEL
> packages, not FlightCtl packages. They are resolved from your existing RHEL
> subscription repos and do not require the FlightCtl repo.

To pin to a specific FlightCtl release version, include the version number:

```bash
dnf download --resolve --alldeps --destdir ~/flightctl-rpms \
    flightctl-agent-1.1.2 flightctl-cli-1.1.2 \
    open-vm-tools ignition afterburn cloud-init
```

To see which versions are available:

```bash
dnf list --showduplicates flightctl-agent
```

> [!NOTE]
> If you need a full offline repo that lets the target machine resolve packages with
> `dnf install` instead of installing from flat `.rpm` files, use `dnf reposync`
> instead. See [Setting up a local RPM repository](offline-rpm-repository.md) for both
> approaches and when to choose each.

Verify that the downloaded files include the expected packages:

```bash
ls ~/flightctl-rpms/ | grep -E 'flightctl-(agent|cli)|open-vm-tools|ignition|afterburn|cloud-init'
```

### Step 2: Transfer the packages to the target machine

Package the downloaded RPMs into an archive:

```bash
tar -czf ~/flightctl-agent-rpms.tar.gz -C ~ flightctl-rpms/
```

Transfer the archive to the target machine. The transfer method depends on your
environment — see [Packaging artifacts for portable media](offline-portable-media.md)
for full instructions. For a target reachable via a jump host:

```bash
scp ~/flightctl-agent-rpms.tar.gz <user>@<target_host>:~/
```

### Step 3: Install on the target machine

On the target machine, extract the archive:

```bash
tar -xzf ~/flightctl-agent-rpms.tar.gz -C ~/
```

Install all packages from the local directory:

```bash
sudo dnf install -y ~/flightctl-rpms/*.rpm
```

> [!NOTE]
> If `dnf` reports unresolved dependencies, the prep machine may be running a different
> RHEL minor version than the target. Re-run `dnf download --resolve --alldeps` on a prep machine
> that matches the target's exact RHEL version and rebuild the archive.

### Step 4: Configure the agent

The agent reads its configuration from `/etc/flightctl/config.yaml`. At minimum, you
must configure the URL of the Flight Control enrollment service:

```bash
sudo tee /etc/flightctl/config.yaml << 'EOF'
enrollment-service:
  server: https://<flightctl_service_host>:7443
  authentication:
    client-certificate-file: /etc/flightctl/certs/agent.crt
    client-key-file: /etc/flightctl/certs/agent.key
    ca-file: /etc/flightctl/certs/ca.crt
EOF
```

Replace `<flightctl_service_host>` with the hostname or IP address of the Flight Control
service. For full configuration options including TPM, labels, and audit settings, see
[Configuring the Flight Control Agent](installing-agent.md).

The CA certificate and enrollment credentials must be distributed to the device through
your provisioning pipeline before the agent can connect.

### Step 5: Start and enable the agent

Enable and start the `flightctl-agent` service:

```bash
sudo systemctl enable --now flightctl-agent
```

### Step 6: Verify the installation

Check that the agent service is running:

```bash
systemctl status flightctl-agent
```

Verify the CLI is available:

```bash
flightctl version
```

Check the agent logs for enrollment activity:

```bash
journalctl -u flightctl-agent -f
```

---

## Part 2: Making container images available for managed workloads

The `flightctl-agent` service runs without container images, but the workloads it
manages (containerized applications defined in the device's Fleet template) need OCI
images to be available on the device at runtime. Two approaches are supported.

### Option A: Local container registry on the target device

Run a local container registry on the target machine and pre-load workload images
into it before the agent applies any workload configurations. The device spec in
Flight Control points at the local registry instead of an internet-accessible registry.

1. On the target machine, start a local container registry:

   ```bash
   mkdir -p ~/registry-data
   podman run -d --name local-registry \
       --network=host \
       --security-opt label=disable \
       -v ~/registry-data:/var/lib/registry \
       --restart=always \
       docker.io/library/registry:2
   ```

   > [!NOTE]
   > Use `--network=host` on RHEL 9 to avoid port-forwarding issues with rootless
   > podman. Use `--security-opt label=disable` instead of the `:z` volume flag when
   > `--network=host` is set.

2. Configure the system to redirect pulls from upstream registries to the local registry.
   Edit `/etc/containers/registries.conf`:

   ```toml
   [[registry]]
   prefix = "quay.io"
   location = "localhost:5000"
   insecure = true

   [[registry]]
   prefix = "docker.io"
   location = "localhost:5000"
   insecure = true
   ```

3. On the prep machine, use `flightctl-mirror-images` to create a bundle of the required workload
   images and transfer it to the target:

   ```bash
   ./bin/flightctl-mirror-images --variant community-el9 --bundle ~/workload-images.tar.gz
   scp ~/workload-images.tar.gz <user>@<target_host>:~/
   ```

   See the [air-gap mirroring guide](../../../scripts/air-gap/README.md) for full
   `flightctl-mirror-images` options and bundle transfer steps.

4. On the target machine, extract the bundle and run `import.sh` to push the images
   into the local registry:

   ```bash
   mkdir ~/flightctl-bundle
   tar -xzf ~/workload-images.tar.gz -C ~/flightctl-bundle
   cd ~/flightctl-bundle
   ./import.sh
   ```

### Option B: Pre-pulled images loaded directly into podman

If the workload images are few and known in advance, you can export them from the
prep machine and load them directly into podman on the target, without running a
local registry.

1. On the prep machine, pull and save each image:

   ```bash
   podman pull quay.io/<org>/<image>:<tag>
   podman save -o ~/my-app-image.tar quay.io/<org>/<image>:<tag>
   ```

2. Transfer the saved image archive to the target machine:

   ```bash
   scp ~/my-app-image.tar <user>@<target_host>:~/
   ```

3. On the target machine, load the image into podman's local storage:

   ```bash
   podman load -i ~/my-app-image.tar
   ```

4. Verify the image is available:

   ```bash
   podman images
   ```

   The agent can then start containers from images in podman's local storage without
   any network access.

## Next steps

- [Configuring the Flight Control Agent](installing-agent.md) — full configuration
  reference for `config.yaml`
- [Setting up a local RPM repository](offline-rpm-repository.md) — `dnf reposync`
  and `dnf download` approaches for creating offline RPM sources
- [Packaging artifacts for portable media](offline-portable-media.md) — USB, tar,
  and other transfer formats

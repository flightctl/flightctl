# Building the imagebuilder VM runtime

The imagebuilder worker runs image builds, bootc-image-builder and Syft inside
a disposable QEMU guest. The worker images include the emulator, firmware and a
prepared guest disk. A running worker does not download or provision its guest.
The VM launcher currently supports x86_64 workers and guests. Other Flight
Control images continue to support their existing architectures.

## Prepare the guest disk

Install `qemu-system-x86_64`, `qemu-img`, `python3`, `curl`, `timeout`, and either
`genisoimage` or `xorriso`. On Debian or Ubuntu, the QEMU packages are
`qemu-system-x86` and `qemu-utils`.

Run the preparation target:

```console
make imagebuilder-vm-guest
```

The target downloads Fedora Cloud 43's versioned x86_64 image and verifies its
SHA-256 against the literal recorded in the preparation script. The checksum
comes from the [Fedora release checksum file](https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-43-1.6-x86_64-CHECKSUM).
To reuse a local copy of that same source image, set `VM_GUEST_SOURCE_IMAGE`.
A replacement distribution must supply `VM_GUEST_SOURCE_URL` and
`VM_GUEST_SOURCE_SHA256` together and satisfy the guest contract below.

Preparation boots a writable overlay in QEMU, updates the guest kernel and
packages, and installs the tools needed by the launcher. KVM is used when it is
accessible; otherwise preparation uses TCG. Set `VM_GUEST_ACCELERATOR=tcg` to
force software emulation. The default preparation deadline is 45 minutes;
`VM_GUEST_PREPARATION_TIMEOUT` overrides it.

The process flattens the disk only after successful provisioning and shutdown.
Build outputs under `.output/flightctl-vm/` are:

- `guest.disk`: prepared qcow2 disk, without a backing-file dependency.
- `guest.sha256`: checksum of that disk.
- `guest.rpms`: installed package versions.
- `guest-source.json`: source URL and checksum.

The source image and procedure are pinned. Fedora package repositories can
change, so two provisioning runs need not produce identical bytes or package
versions. Archive the prepared disk, checksum and package manifest together for
release rebuilds. Do not silently replace an archived release artifact by
reprovisioning it against changed repositories.

## Build the worker image

Build the worker after guest preparation:

```console
make flightctl-imagebuilder-worker-container OS=el9
```

Use `OS=el10` for the Enterprise Linux 10 userspace. The image target automatically
runs guest preparation if its output does not exist. Direct Containerfile builds
must run `make imagebuilder-vm-guest` first.

Each Containerfile compiles QEMU, libslirp and xorriso from versioned upstream
archives verified by SHA-256. Compilation uses the same UBI userspace as the
worker. QEMU explicitly enables KVM, TCG, virtio-9p and user networking; build-time
probes reject an emulator missing the 9p device or user network backend. Stock
Enterprise Linux QEMU builds must not be substituted without checking these
features.

The tools install under `/usr/local/bin`, with QEMU firmware under
`/usr/local/share/qemu`. A `genisoimage` wrapper invokes xorriso's mkisofs
compatibility mode. The prepared disk is installed read-only at
`/usr/share/flightctl-vm/guest.qcow2`. Container image publishing excludes the
imagebuilder worker from the arm64 matrix until the launcher supports that
architecture.

## Guest contract

A replacement guest must provide:

- An x86_64 kernel and modules for virtio block/network and 9p2000.L.
- NoCloud cloud-init that installs the per-attempt root SSH public key.
- OpenSSH server, rootful Podman, buildah, `qemu-img`, ext4 tools and mount tools.
- A writable root disk overlay and support for an additional scratch disk.

The runner supplies a fresh root overlay, scratch disk and SSH key per attempt.
Only job input and output directories are shared. Registry passwords, custom
CAs and entitlement material are scoped to the attempt. Worker database,
signing, encryption and service-account credentials remain outside the guest.

BIB and Syft container references remain configurable and are pulled inside the
guest. Guest preparation does not bake repository credentials into the image.
OpenShift KVM execution still requires validation on a node with the configured
KVM device-plugin resource; image builds alone do not prove cluster admission or
device access.

## Local kind and e2e bootstrap

Use the cluster wrapper for local deployments and CI:

```console
test/scripts/create_cluster.sh
```

It checks that the current user can access `/dev/kvm`, mounts the device into the
kind node, and installs the version-pinned test KVM device plugin in
`kube-system`. Both supported kind configurations contain the optional device
mount. Set `KIND_CONFIG=deploy/kind.yaml` to select the smaller development
configuration. The shared backend deployment action uses the same wrapper.

The plugin advertises `devices.anza-labs.dev/kvm`. Bootstrap waits for a positive
allocation before Helm can install the VM worker. `test/scripts/deploy_with_helm.sh`
also checks existing clusters, so clusters created before the KVM mount was
added fail with an actionable recreation message instead of leaving the worker
Pod pending. This privileged plugin is test infrastructure; the application
Helm chart never installs it or changes host security policies.

On a host without KVM, explicitly disable the imagebuilder worker:

```console
IMAGEBUILDER_WORKER_ENABLED=false make deploy
```

For development ImageBuild on an x86_64 host, select software emulation instead:

```console
IMAGEBUILDER_VM_ACCELERATOR=tcg make deploy
```

These settings select the same cluster bootstrap and Helm configuration in the
local scripts and shared CI action. The wrapper omits the KVM mount in either
mode. TCG does not support ImageExport. On other host architectures, disable the
worker. Guest-image preparation can use TCG independently of the deployed
worker's accelerator.

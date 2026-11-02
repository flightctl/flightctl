# Image task virtual machines

The imagebuilder worker starts a disposable QEMU guest for every ImageBuild and
ImageExport attempt. Container execution, including SBOM scanning and container
disk wrapping, takes place in the guest. The worker retains queue handling,
resource status, enrollment credential generation, and OCI artifact publication.

## Guest lifecycle

`internal/imagebuilder_worker/vm` owns QEMU, SSH control, a private writable
qcow2 overlay, and a guest-local scratch disk. It boots a prepared, read-only
base disk. Each session receives an ephemeral SSH key and its own loopback
forwarding port. Closing a session terminates and reaps QEMU before removing
its disks and key. Task cancellation also terminates active SSH commands.
The boot deadline, absolute execution deadline, and output-based inactivity
limit have separate purposes. Launcher polling does not generate synthetic
progress that could conceal a stalled task.

The initial implementation supports Linux amd64 workers. ImageExport requires
KVM. TCG is an explicit development option for ImageBuild. A cluster operator
supplies KVM through a device plugin; the Flight Control chart requests its
extended resource and does not install the plugin or expose host devices through
hostPath volumes. Live OpenShift KVM execution requires separate validation.

## Inputs and outputs

The worker shares only the generated build context and an output directory with
the guest through 9p. The build context is read-only in the guest. Registry
credentials and subscription material are staged separately into guest-local
files. Database credentials, signing keys, encryption keys, and the worker's
service-account token are not shared. Source and destination registry logins use
separate authentication files, including when their registry hostnames match.

Container storage and osbuild output live on the guest's ext4 scratch disk.
ImageBuild pushes directly from the guest and returns its registry digest.
ImageExport copies final output through 9p. The worker rejects symbolic links,
confines paths to the output directory, and compares the guest's SHA-256 with
the received file before publication. Container disks are returned as an OCI
layout. The output remains available after the guest exits until the existing
Go OCI uploader completes.

## Runtime packaging and validation

The worker images contain QEMU built against their Enterprise Linux userspace,
with 9p and user networking enabled, and a prepared Fedora Cloud guest. Guest
preparation happens during image construction rather than during task execution.
See [the VM packaging instructions](../imagebuilder-vm.md) for the source
pins, preparation procedure, and package manifest.

The agreed behavioral seams are the VM session interface, the worker's
`Consume` method and service-visible outcomes, and rendered Helm workloads.
Unit tests fake external executables at the process perimeter. An optional
local smoke test exercises real KVM boot, SSH and 9p checksum validation:

```console
FLIGHTCTL_VM_SMOKE_IMAGE=/absolute/path/to/guest.qcow2 go test ./internal/imagebuilder_worker/vm -run TestRealGuest -v -count=1
```

That smoke test does not establish Kubernetes admission, OpenShift SCC behavior,
or interoperability with a registry. The full imagebuild e2e workflow remains
the validation path for authenticated build, export publication and device boot.

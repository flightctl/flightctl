# Rootless deployment and local workflows

This document records rootless support in FlightCtl's deployment and ImageBuilder paths, and how the local Make and E2E workflows use that support. It covers the current design, host prerequisites, limitations, and remaining runtime validation.

## Supported deployment paths

- **Quadlets:** A regular-user deployment uses that user's rootless Podman store, user systemd manager, and XDG paths. UID 0 continues to use system systemd, the rootful Podman store, and system paths. The same Quadlet source files serve both managers.
- **Kind:** The local Kind cluster can run on the invoking user's rootless Podman store. Its Helm deployment configures the ImageBuilder worker for the rootless export path and mounts KVM instead of the broad host device paths used by rootful export.
- **ImageBuilder:** The worker has rootless execution paths for ImageBuild and a KVM-backed native `--in-vm` ImageExport. The worker is deployed by both Kind and Quadlets, so these paths apply to both. The worker service and BIB job-container privilege settings remain as documented under [Image-builder constraint](#image-builder-constraint).

The local Make targets select and exercise these paths based on effective UID; they do not introduce a separate rootless mode flag.

## Execution contract

The effective UID selects the execution scope. A non-root invocation uses that account's rootless Podman store, user systemd manager, XDG paths, and libvirt session. An invocation as UID 0 keeps the rootful Podman store and system services. There is no `ROOTLESS` Make variable or mode flag; run the command as the user whose resources it should use.

Rootless cleanup only removes resources in the invoking user's scope. It cannot remove root-owned containers, system Quadlets, or files. Run cleanup as root to clean root-owned deployment state.

## Local workflow coverage

| Target | Rootless behavior | Remaining validation |
|---|---|---|
| `make deploy` | Uses rootless Podman kind, checkout-local kind and Helm binaries, and a delegated user cgroup scope. The worker receives `/dev/kvm` through a dedicated device mount for rootless ImageExport. | Run a complete deployment, then exercise worker ImageBuild and ImageExport jobs. |
| `make clean` | Deletes the current user's kind cluster and E2E images and cleans user-scope Quadlets. | Verify cleanup after each rootless workflow and confirm root-owned state is untouched. |
| `make deploy-quadlets` | Renders units and configuration under XDG paths, controls `systemctl --user`, and uses the same Podman store that built the images. The API endpoint uses host port 9443, mapped to the service's container-side API listener on 8443. | Run deploy, service health, E2E, and cleanup on a supported user systemd host. |
| `make prepare-e2e-test` | Builds RPMs and E2E images as the invoking user, builds bootc QCOW images with the native `image-builder build --in-vm` CLI inside rootless Podman, and injects files with `virt-customize`. | Run from a clean workspace, including the Brew RPM extraction path if used. |
| `make run-e2e-test` | Uses rootless Podman for Testcontainers. VM-backed specs use libvirt's `qemu:///session`; API-only selections do not need local VM runtime access. Host-side Quadlet operations use the target account's systemd scope. | Run the full default suite, including ImageBuild and ImageExport cases. |

The all-in-one `make e2e-test` path uses the same identity-selected behavior and preflights before its preparation and deployment steps.

Kind and Quadlet deployments cannot be active on the same host at the same time: both publish host ports `3443`, `4317`, `7443`, `7444`, and `8445`. The Quadlet API host port (`9443` for a regular user or `443` for UID 0) avoids Kind's `8443` mapping, but does not avoid those other collisions. Run `make clean-cluster` before deploying local Quadlets, or `make clean-quadlets` before creating the local Kind cluster. This constraint also applies to rootful deployments.

## Host prerequisites

The non-root targets need the following host setup:

- Rootless Podman must work locally for the invoking user. Remote Podman connections are rejected because images in a remote store are not visible to local systemd Quadlets. The host needs subordinate UID and GID ranges in `/etc/subuid` and `/etc/subgid`.
- Rootless Quadlet ImageExport and bootc QCOW preparation use `--group-add=keep-groups` to pass KVM device access, so Podman must use `crun`. Rootless Kind also uses a Podman wrapper while creating node containers so host supplementary groups are retained. The KVM group may not map into the nested ImageBuilder worker, so a named-user ACL on host `/dev/kvm` for the invoking UID, with an effective read/write mask, may be needed. Applying that ACL may require administrator or udev configuration, and recreating the device can remove it. The ACL grants device-file DAC access only; SELinux and device-cgroup restrictions still apply.
- Rootless Kind and Quadlet workers require unified cgroup v2. Rootless kind also needs a user systemd manager and permission to create a delegated user scope.
- `/dev/kvm` must be readable and writable by the user. Rootless ImageExport uses it through native `image-builder --in-vm`, E2E preparation uses it for bootc QCOW creation, and VM-backed E2E specs use it for local VMs. API-only E2E selections do not need local KVM, a running libvirt service, `virsh`, or a `qemu:///session` connection. Suites that import the shared E2E harness still compile its libvirt CGo binding, so the default CGo build needs libvirt development headers and `pkg-config` metadata. Rootless ImageBuild itself runs Podman directly and does not use KVM.
- E2E preparation needs `virt-customize`. VM-backed E2E specs need a working `qemu:///session` libvirt connection. Host `virsh` is used for best-effort VM cleanup and is not required to run tests.
- RPM builds use Mock's unprivileged/user-namespace mode. The Packit builder still runs in a Podman container with `--privileged`; this is separate from Mock's `isolation=simple` setting, and removing it has not been validated. Under rootless Podman, that container remains bounded by the invoking user's namespace, but the flag disables normal container isolation. Brew RPM extraction needs `rpm2cpio` and `cpio`.
- The checkout's `bin/` directory must be writable by the invoking user because Make builds local binaries there. If a root invocation created a root-owned build directory, run `make clean-all` as root or restore write access before switching to rootless builds.
- Quadlet deployment needs an active user systemd manager. Enable user lingering through the host administrator if services must continue after logout.
- On enforcing SELinux hosts, rootless bootc-image-builder may need the upstream osbuild SELinux policy installed.
- If the user was recently added to the KVM group, refresh both the login session and the lingering systemd user manager before rootless Quadlet deployment.

Run the matching lightweight preflight directly, or let the Make target run it:

```bash
test/scripts/runtime_preflight.sh kind
test/scripts/runtime_preflight.sh e2e-prepare
test/scripts/runtime_preflight.sh e2e-run
test/scripts/runtime_preflight.sh quadlets
test/scripts/runtime_preflight.sh clean
```

The preflight reports missing host prerequisites without invoking `sudo` or changing persistent system configuration. The `kind` preflight briefly probes whether systemd can create a delegated user scope. Its `/dev/kvm` check verifies host-user access only; it cannot prove access from the ImageBuilder Pod nested in the rootless Kind node. The worker's direct `O_RDWR` open check at ImageExport time is the definitive permission check.

## Image-builder constraint

The image-builder worker runs in the kind cluster as well as in the Quadlet deployment. Rootless ImageBuild runs Podman directly in the worker and does not require KVM. Rootless QCOW2/VMDK ImageExport uses the unified native `image-builder build --in-vm` CLI shipped in the configured builder image and requires KVM. The pinned v83 CLI sets `InVm` only for the `image` pipeline; its generic ISO target uses a different pipeline and also differs from the existing rootful BIB `iso` output. Rootless ISO ImageExport is therefore rejected until there is a supported, equivalent in-VM path.

These settings control different layers:

| Setting | What it controls | Current implementation |
|---|---|---|
| Rootless Podman | Which host user owns the Podman invocation and image store | Make targets run as the caller; no `sudo` or mode flag is added. |
| BIB `--in-vm` | Where the OSBuild image pipeline runs | Used for rootless QCOW2/VMDK exports; it does not change container privilege settings. |
| BIB job-container `--privileged` | Isolation and device access for the container running BIB | Retained, as in the upstream v83 rootless example. |
| ImageBuilder worker privilege | Privileges on the worker service container/Pod | Retained in Quadlet and Helm; `--in-vm` does not remove it. |

Here, “rootless” means that the host user runs Podman without `sudo` and uses that user's image store. It does not mean that every container in the build path omits `--privileged`. The upstream v83 rootless example retains `podman run --privileged`; rootless Podman still limits that container to the invoking host user's permissions. In native v83 `image-builder`, `build --in-vm` selects only the `image` OSBuild pipeline to run in a KVM guest (`InVm: ["image"]`); it does not put the whole BIB process or worker in a VM, or change Podman's security flags. The current BIB job-container command retains `--privileged`, as in the upstream example; a narrower job-container profile has not been validated. When the native CLI's container setup runs, it changes SELinux labels and mounts a tmpfs and bind mount for `osbuild` outside the OSBuild VM; `--in-vm` skips the `devtmpfs` mount on `/dev`. The BIB compatibility build path explicitly rejects rootless Podman when `inVm` is false, but its CLI does not accept `--in-vm`; the rootless implementation therefore uses the native CLI. The imagebuilder-worker's own privileged setting in Quadlet/Helm is a separate layer; `--in-vm` alone does not establish whether that can be removed, since the worker also runs nested Podman and has a rootful execution path. The kind configuration exposes only `/dev/kvm` instead of broad host devices.

The v83 README's rootless example passes `--in-vm` to the `bootc-image-builder` compatibility entrypoint, but the pinned v83 source does not support that invocation: the compatibility parser does not define `--in-vm`, and its build path calls `setup.Validate(..., false)`, which rejects rootless Podman. The BIB image installs the unified binary at `/usr/bin/bootc-image-builder`; its `main()` selects the compatibility CLI when `argv[0]` ends in `bootc-image-builder` and the native CLI otherwise. The preparation script and worker therefore symlink that binary to `/tmp/image-builder` and invoke the native `image-builder build` CLI. This dispatch is required to use the native bootc-ref and `--in-vm` flags in v83. The native CLI's flag check does not prove that the pinned image successfully produces an artifact or that its output and filesystem behavior match the existing rootful compatibility path. Rootless QCOW2/VMDK support remains experimental and unverified at runtime; rootless ISO export is not supported by the current implementation. Kind v0.26's Podman provider does not pass supplementary host groups to node containers by default, so the rootless cluster creator injects `--group-add=keep-groups` for Kind node containers. A named-user `/dev/kvm` ACL can bridge host UID access when the KVM GID is unmapped, subject to host ACL support and the device access policy. Rootless support for the full service is not proven until both ImageBuild and a supported ImageExport succeed through the worker in rootless Kind and Quadlet deployments.

If nested execution fails in kind, keep the worker's job interface and evaluate a rootless build executor outside the cluster. Disabling the worker can be useful for app-only development, but does not count as full service support.

## Scope and cleanup details

- Rootless Quadlet units, writable configuration, certificates, binaries, temporary builds, and service data use XDG-backed user paths. Grafana and Prometheus data use `${XDG_STATE_HOME}/flightctl/{grafana,prometheus}` through rootless-only mount drop-ins; system units retain `/var/lib/{grafana,prometheus}`. Shared Quadlet files use systemd path specifiers so the active system or user manager selects the matching path. Default-start units use shared `WantedBy=default.target` directives; core services remain attached to `flightctl.target`, and user-scope wants links start the two native service targets when the user manager starts. An image-builder storage drop-in selects `/var/tmp` in system scope or XDG cache in user scope.
- The service's container-side API listener uses port 8443 in both scopes. Quadlets publish it on host port 9443 for an unprivileged user and 443 for UID 0. Kind uses host port 8443 for the Alertmanager proxy, so rootless Quadlets use a different host port. The process identity selects the host port; there is no rootless mode option.
- Existing E2E settings select remote targets: `E2E_SSH_HOST`, `E2E_SSH_USER`, and the SSH credential variables. A non-local SSH host infers Quadlet; `E2E_API_ENDPOINT` overrides the API URL and port.
- Rootless E2E registry configuration is stored in the user's containers configuration directory. Host-side cleanup uses the current user's libvirt session; guest-side `sudo` remains where a test checks privileged device behavior.
- E2E QCOW injection uses libguestfs instead of attaching and mounting the disk on the host. It must preserve bootc `/etc`, agent configuration, certificates, registry trust, remaps, and host entries.
- `make clean` and `make clean-quadlets` operate on the current UID's kind, Podman, libvirt, and Quadlet resources. They do not cross into another user's Podman or systemd scope.

## Acceptance checklist

Rootless development is ready for general use when these checks pass on a supported Linux host:

1. Run `make deploy` as a regular user and confirm the kind cluster and services run without host-side `sudo`.
2. Submit ImageBuild and ImageExport jobs through the worker deployed in rootless kind.
3. Run `make prepare-e2e-test` from a clean workspace and confirm RPM, container, QCOW, and injection artifacts belong to the user.
4. Run `make run-e2e-test`, including the default image-builder cases, and confirm its containers and VMs are user-scoped.
5. Stop the local Kind cluster with `make clean-cluster`, then run `make deploy-quadlets`, verify services and E2E access on host port 9443, and run `make clean` to confirm user services, secrets, volumes, and generated files are removed.
6. Run the rootful targets as UID 0 and confirm the existing systemd, Podman, and port-443 behavior remains available.

The effective UID selects the scope for every command; no mode variable is needed. The renderer installs a gateway port drop-in for the selected scope and an image-builder KVM/group drop-in only for a user manager.

## References

- [kind rootless guide](https://kind.sigs.k8s.io/docs/user/rootless/)
- [Podman Quadlet documentation](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)
- [bootc-image-builder v83 rootless instructions](https://github.com/osbuild/image-builder/blob/v83.0.0/bootc-image-builder/README.md#rootless)
- [Podman privileged-container behavior](https://docs.podman.io/en/stable/markdown/podman-run.1.html#privileged)
- [native image-builder v83 CLI source](https://github.com/osbuild/image-builder/blob/v83.0.0/cmd/image-builder/main.go)
- [native v83 `--in-vm` flag definition](https://github.com/osbuild/image-builder/blob/v83.0.0/cmd/image-builder/cmd.go)
- [v83 compatibility CLI flag and build setup](https://github.com/osbuild/image-builder/blob/v83.0.0/cmd/image-builder/bib_cmd.go)
- [v83 compatibility build validation](https://github.com/osbuild/image-builder/blob/v83.0.0/cmd/image-builder/bib_main.go)
- [v83 BIB image entrypoint](https://github.com/osbuild/image-builder/blob/v83.0.0/bootc-image-builder/Containerfile)
- [v83 rootless and in-VM setup checks](https://github.com/osbuild/image-builder/blob/v83.0.0/pkg/setup/setup.go)
- [image-builder v83 bootc image type definitions](https://github.com/osbuild/image-builder/blob/v83.0.0/data/distrodefs/bootc-generic/imagetypes.yaml)
- [bootc-image-builder issue: Missing xattrs reference with `--in-vm`](https://github.com/osbuild/image-builder/issues/2506)
- [virt-customize documentation](https://libguestfs.org/virt-customize.1.html)
- [containers/image registry configuration](https://github.com/containers/image/blob/main/docs/containers-registries.conf.d.5.md)

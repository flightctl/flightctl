# Flight Control Agent Architecture

## Overview

The Flight Control agent runs on each managed device, responsible for establishing device identity, reconciling the DeviceSpec, and reporting status to the management service. The agent is designed to have a small footprint and be a responsible steward of device resources.

## Design Principles

**Declarative API.** The agent reconciles against a desired state (DeviceSpec) rather than executing imperative commands. The DeviceSpec’s scope is device management (host OS & config) and application management (containerized workloads).

**Update safety.** The agent must minimize change risk (system becoming non-functional or unmanageable) and service outage by locally staging dependencies before making any changes and applying / rolling back changes transactionally.

**Active component.** The agent actively monitors the health and update progress and reports these to the service (circuit-breaking for update rollouts, live user feedback).

**Thin orchestration layer.** The agent coordinates existing system tools (bootc, podman, systemd) rather than reimplementing functionality. This keeps the agent small and simple to debug.

**Strong cryptographic identity.** Every device has a unique X.509 certificate-based identity established during enrollment, with private keys stored in TPM when available.

**Zero-trust communication.** All agent-to-service communication uses mTLS with optional TPM-backed private key operations.

**Escape hatches.** Operators can extend and troubleshoot via hooks (before/after reconciliation) and remote console debugging without opening device ports.

## Secure Device Lifecycle Management

The agent manages the complete secure lifecycle from enrollment based on attestation of a TPM-bound device identity through to decommissioning.

| State | Description |
| ----- | ----------- |
| Factory | No identity; awaiting enrollment |
| Enrolled | TPM-bound identity established; certificate issued |
| Managed | Active reconciliation; status reporting; continuous cert rotation |
| Decommissioned | Identity revoked; device wiped or repurposed |

### Enrollment

1. Agent generates (optional) TPM-bound key pair (private key never leaves TPM)

2. TPM handshake proves device identity and in the future integrity

3. Agent submits CSR to management service

4. Service validates request and signs certificate

5. Agent begins mTLS communication

### Certificate Management

Cert Manager handles on disk and TPM-bound mTLS certificates for the agent and adjacent services:

* **Agent certificate**: mTLS for agent-to-service communication

* **Telemetry certificate**: mTLS for OTel collector-to-service communication

* **Rotation**: Continuous automated renewal without re-enrollment

## Communication

All communication uses mTLS:

* **Spec & Status**: HTTP long-polling for DeviceSpec updates and status push

* **Console**: Bidirectional gRPC streaming for remote terminal access

## Reconciliation Controller

The agent reconciles the **DeviceSpec** against device state by calling external tools:

| Layer | Tool | Description |
| ----- | ---- | ----------- |
| OS Image | bootc | Image-based updates with rollback |
| OS Config | MCD-like | Files, systemd units, etc. |
| Application Workloads | podman, helm | Containers and Helm charts |

### Flow

1. Fetch DeviceSpec from management service

2. Execute **before hooks** (can abort on failure)

3. Prefetch OCI images and artifacts

4. Apply changes via system tools

5. Execute **after hooks** (validation, notification)

6. Rollback to previous known good version on error

7. Report status and alerts

Podman application monitors rebuild their in-memory registrations during reconciliation after an agent restart. When an application changed while the agent was stopped, its update action replaces the existing workloads and registers the desired application for monitoring.

For each application status report, the agent inspects available installed image-backed volumes and builds the delta snapshot from their observed runtime identities. Podman image volumes are checked against their immutable image ID and digest labels; artifact volumes are matched to their recorded creation time and artifact binding. Missing or inconsistent identities make the snapshot incomplete. Status reporting does not resolve a mutable image reference in place of observing the volume.

Application delta status uses one owned image snapshot for each application update. It includes observed workload images, required image-backed volumes, and an applicable Quadlet parent OCI image or artifact. Helm chart packages and Compose bundle references remain visible in reported image digests but do not join the delta-outcome snapshot. Each identity retains its registry reference, digest, OCI type, and storage owner. A volume identity is tied to the volume's creation time and the application lifetime, so replacing the volume cannot reuse evidence from its earlier contents.

Verified volume observations are reused between status cycles and cleared when an application is removed or reinstalled. A volume image does not need to appear as a workload image to participate in the complete snapshot.

The agent installs retained parent contents with their observed digest. A cached digest never replaces the identity reported by installation. A failed artifact-volume identity write is logged after extraction and does not discard successfully prepared contents. Existing bindings must still be invalidated before changing volume contents.

History protects the previous complete observed set and the current update. Replacing pending content behind a mutable reference releases superseded content for eviction, including when observation is incomplete. OS history protects the booted identity and the latest accepted identity for the active target. Protected records may temporarily exceed the capacity limit.

## Update Policy

* **Maintenance windows**: Independently define windows for download, update, and install operations

* **Rollout strategy**: Gradual rollout with health checks

* **Rollback triggers**: Conditions for automatic rollback

* **Circuit breaking**: Halt rollouts when failure thresholds exceeded

## Status Reporting

The agent continuously reports device health, application state, and resource conditions to the management service. Built-in collectors capture system info (hostname, OS, hardware, network) while user-defined custom collectors enable site-specific data. This powers fleet-wide visibility, rollout decisions, and alerting.

OS image digests come from bootc. Application workload digests come from the owning Podman user store or the configured CRI runtime, including nested workload images. Image-backed volumes use the application's Podman user store. Applications without an explicit `runAs` use the agent-process user context.

Podman can report an index digest on a running container while inspecting its immutable local image ID returns the selected platform manifest digest. The index may also appear in the image's `RepoDigests`. These digest forms are not treated as equal for `lastDelta` matching. Until status collection can verify their relationship for the observed image, the agent reports the observed digest and withholds `lastDelta` when a stored result uses the other digest.

For Kubernetes workloads, a registry-qualified runtime image ID identifies the observed manifest. For opaque image IDs, the agent queries CRI for a matching repository digest. If no verified registry digest is available, status leaves the image digest empty, including for digest-pinned desired references. When CRI reports multiple distinct matching repository digests for an opaque image ID, their order does not establish an observed identity; status leaves that observation unknown. The agent reuses ambiguous repository metadata for repeated events with the same runtime image identity. Missing repository metadata and failed inspections are retried on later events. An opaque runtime ID is never reported as a registry digest. Runtime observations take precedence over cached parent digests for the same image reference.

Podman image deltas use the same user store as the application, including native Quadlet VM images and image-backed volumes. The agent reads each delta's source digest from its artifact metadata and checks that source in the owning store. Discovery selects the smallest locally usable delta by total download size, with lexical reference order breaking ties.

The agent tracks prefetch tasks and saved delta outcomes by image reference, storage owner, and OCI target type. When Podman and CRI workloads request the same image reference, each store is checked and updated independently.

Helm chart packages continue through the chart cache. Rendered workload images use the configured CRI runtime store for delta source checks and imports. CRI-O imports into its container storage; containerd imports through its configured socket into the Kubernetes namespace.

Digest-pinned CRI workload images remain eligible for deltas. Source checks use registry digests recorded in CRI, including source images from another repository. Discovery selects the smallest locally usable artifact, with lexical reference order breaking ties.

After importing a reconstructed image, the agent refreshes the exact requested registry reference, including digest-pinned CRI references, before reporting `Applied`. An unusable hint or failed delta reports `Fallback` with a reason and attempts a full pull. Successful recovery allows reconciliation to continue.

The refresh attaches the resolved target digest to the original repository name. Unqualified image names remain unqualified so the runtime can apply configured registry aliases.

For OS updates, the agent reuses a desired image that is already booted or cached without verifying its digest against the registry. Only an image that needs fetching enters delta discovery or full-image prefetch. Without a usable delta, prefetch uses `PullIfNotPresent` before bootc stages the desired image. A failed required pull stops staging.

Top-level Compose bundles and Helm chart packages use regular preparation, ignore image-delta hints, and contribute neutral `NotRequired`. They do not participate in the workload delta snapshot. Nested workload images remain eligible for deltas. A hinted Quadlet parent artifact reports `Fallback` with a reason before following its regular artifact pull policy. Application results report `Partial` when an `Applied` target accompanies a `NotUsed` or `Fallback` target; `NotRequired` targets remain neutral. Multiple fallback reasons use the lexically first reason.

Image digests remain separate from `lastDelta`, which contains only the delta outcome and optional fallback reason.

The agent persists OS and application delta outcomes in `delta-status.json` in its data directory and restores them after restart. This file stores delta results separately from application monitor registrations.

OS outcomes are associated with the image reference and digest observed by bootc after staging. For delta-staged images, the agent retries this verification before reboot, allowing a transient staging-status failure to recover its outcome association. Status selects the result for the booted image and digest. Preparing another image retains the previous booted result, including across restart. An unknown booted identity or a result without a verified association omits `lastDelta`.

When the desired OS image is already booted, the agent preserves a saved outcome only for that exact image reference and digest. Otherwise, it records `NotRequired` for the booted identity. A different digest behind the same tag does not inherit an earlier `Applied` or `Fallback` outcome.

After restart, the agent restores verified OS image history and starts a fresh update attempt. Outcomes saved only against an image reference are not reused for newly staged content, including cached images and full-image fetches.

For application targets using `PullIfNotPresent`, a locally present image is reused without resolving its mutable tag against the registry. Only a missing target enters delta discovery or a full pull. When a missing target's source digest confirms that its tag now refers to different content, the agent clears the result for the active prefetch attempt before preparing that content. Saved results remain associated with the verified digest of the image still used by workloads; an unavailable digest inspection does not invalidate saved history.

Application outcomes retain per-image associations with a verified registry digest, storage owner, reference, and OCI target type. Status combines matching recorded outcomes from the complete observed workload image set, including old and new versions during a rollout. Shared images reuse their recorded outcome. Preparing a new desired image retains recent history across restart. Status omits outcomes for identities whose history has been evicted.

Unknown workload identities, missing expected images, unmatched results, or incomplete collection omit application `lastDelta`. A known digest from another replica cannot cover an unknown image. Desired prefetch results alone do not establish an observed association. Applications requiring parent or volume evidence also omit the outcome until all required image associations can be verified.

## Ansible Collection

Flight Control provides an Ansible Collection for automating device and fleet management. This enables operators to integrate Flight Control into existing Ansible-based workflows for provisioning, configuration, and orchestration. Enables SSH-less and jumphost-less operation through its remote console connection.

## See Also

* [Flight Control Repository](https://github.com/flightctl/flightctl)
* [Device API Statuses](device-api-statuses.md)
* [Flight Control Ansible Collection](https://github.com/flightctl/flightctl-ansible)

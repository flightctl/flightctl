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

## Update Policy

* **Maintenance windows**: Independently define windows for download, update, and install operations

* **Rollout strategy**: Gradual rollout with health checks

* **Rollback triggers**: Conditions for automatic rollback

* **Circuit breaking**: Halt rollouts when failure thresholds exceeded

## Status Reporting

The agent continuously reports device health, application state, and resource conditions to the management service. Built-in collectors capture system info (hostname, OS, hardware, network) while user-defined custom collectors enable site-specific data. This powers fleet-wide visibility, rollout decisions, and alerting.

## Ansible Collection

Flight Control provides an Ansible Collection for automating device and fleet management. This enables operators to integrate Flight Control into existing Ansible-based workflows for provisioning, configuration, and orchestration. Enables SSH-less and jumphost-less operation through its remote console connection.

## See Also

* [Flight Control Repository](https://github.com/flightctl/flightctl)
* [Device API Statuses](device-api-statuses.md)
* [Flight Control Ansible Collection](https://github.com/flightctl/flightctl-ansible)

# Introduction

## Project Vision

Flight Control aims to provide simple, scalable, and secure management of edge devices and applications. Users declare the operating system version, host configuration, and set of applications they want to run on an individual device or a whole fleet of devices, and Flight Control will roll out the target configuration to devices where a device agent will automatically apply them and report progress and health status back up.

Flight Control is designed for modern, container-centric toolchains and operational best practices. It works best on image-based Linux operating systems running bootc or ostree and with container workloads running on Podman/Docker or Kubernetes.

Features and use cases Flight Control aims to support:

* Declarative APIs well-suited for GitOps management.
  * APIs are Kubernetes-like, so they should instantly feel familiar to Kubernetes users and allow them to reuse tools and toolchains.
  * APIs do not depend on Kubernetes, though, and target a shallow learning curve for non-Kubernetes users.
* Web UI to manage and monitor devices and applications for ClickOps management.
* Fleet-level Management, allowing users to define a device template and management policy for a fleet that the system automatically applies to all current and future member devices.
* Container or VM workloads on Podman using docker-compose or Quadlets, Kubernetes services on MicroShift using kustomize or Helm.
* Agent-based architecture, allowing:
  * scalable and robust management under adverse networking conditions (agent "calls home" when the device has connectivity),
  * autonomous and safe OTA updates (agent downloads and verifies assets before updating, then transactionally updates or rolls back), and
  * timely yet resource-unintensive monitoring of devices and applications (agent notifies service on update progress and alarms).
* A secure yet friction-free device lifecycle (enrollment, certificate rotation, attestation, and decommissioning) based on hardware root-of-trust.
* Pluggable identity/authentication providers (initially Keycloak / Generic OIDC Providers and OpenShift Authentication API)
* Pluggable authorization providers (initially SpiceDB and Kubernetes RBAC)
* Pluggable certificate providers (initially built-in CA and Kubernetes Cert-Manager)

## Concepts

**Device** - A combination of a (real or virtual) machine, operating system, and application workload(s) that function together to serve a specific purpose.

**Device Spec** - A specification of the state (e.g. configuration) the user wants a Device to have.

**Device Status** - A record of the state (e.g. configuration) that the Device is reported to actually have.

**Device Template** - A template for Device Specs that serves to control drift between the configurations of Devices.

**Fleet** - A group of Devices governed by a common Device Template and common management policies.

**Labels** - A way for users to organize their Devices and other resources, for example to record their location ("region=emea", "site=factory-berlin"), hardware type ("hw-model=jetson", "hw-generation=orin"), or purpose ("device-type=autonomous-forklift").

**Label Selector** - A way for users to group or filter their devices and other resources based on their assigned labels, e.g. "all devices having 'site=factory-berlin' and 'device-type=autonomous-forklift').

**Field Selector** - A way for users to filter and select Flight Control objects based on the values of specific resource fields. Field selectors follow the same syntax, principles, and support the same operators as Kubernetes Field and Label selectors.

**Service** -  The Flight Control Service handles user and agent authentication and authorization, device enrollment and inventory, rolling out updates to devices, and rolling up status from devices.

**Agent** - The Flight Control Agent runs on each device and is responsible for securely enrolling into the Service, querying the Service for updates, autonomously applying these updates, and reporting status on the updates and the health of devices back to the Service.

## High-Level Architecture

Flight Control consists of the highlighted components show in the following high-level architecture diagram:

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/flightctl-highlevel-architecture.svg">
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/flightctl-highlevel-architecture-dark.svg">
  <img alt="Flight Control architecture diagram" src="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/flightctl-highlevel-architecture.svg">
</picture>

The Flight Control Service consists of an API server, worker processes (not shown), and a PostgreSQL database for storing inventory and runtime information such as the current target configuration and the reported actual configuration. The API server exposes two endpoints:

* The *user-facing API endpoint* is for users to connect to, typically from the CLI or web UI. Users on this endpoint must authenticate with the configured external authentication service to obtain a JWT token. They can then use this token when making requests via HTTPS.
* The *agent-facing API endpoint* is for agents to connect to and is mTLS-protected. That is, the service authenticates the device based on its X.509 client certificates. The device's unique certificate is bootstrapped during enrollment based on hardware root-of-trust, meaning the private key is protected by the TPM and so the client certificate cannot be used by another entity. Certificates are automatically rotated before they expire.

The Flight Control Service talks to various external systems to authenticate and authorize users, get mTLS certificates signed, or query configuration for managed devices.

The Flight Control Agent is a process running on each managed device. It always "calls home" to the Service, so the device can be on a private network or have a dynamic IP address. The agent handles the enrollment process with the service and periodically polls the Service for a new target configuration. It also periodically sends a heartbeat to the Service and notifies the Service when the device or application status changes relative to the desired target configuration.

When the Agent receives a new target configuration from the Service, it

1. downloads all required assets (OS image, application container images, etc.) over the network to disk, so it doesn't depend on network connectivity during the update,
2. updates the OS image by delegating to bootc (or rpm-ostree),
3. updates configuration files on the device's file system by overlaying a set of files sent to it by the Service,
4. if necessary, reboots into the new OS, otherwise signals services to reload the updated configuration, and
5. updates applications running on Podman or MicroShift by running the necessary commands.

If applying any of these changes fails or the system does not return online after reboot (detected greenboot health-checks and optionally user-defined logic), the agent will rollback to the previous OS image and configuration.

As the target configuration for devices and device fleets is declarative, users can store it in a Git repository that the Flight Control Service can periodically poll for updates or can receive updates from a webhook.

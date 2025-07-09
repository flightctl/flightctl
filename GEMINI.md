# Flightctl Documentation Index

This document provides a comprehensive index of all Markdown documentation within the Flightctl repository. Its purpose is to help developers and users navigate the project's resources efficiently.

## Table of Contents
- [Getting Started](#getting-started)
- [User Documentation](#user-documentation)
- [Developer Documentation](#developer-documentation)
- [API Documentation](#api-documentation)
- [Testing Documentation](#testing-documentation)
- [Packaging Documentation](#packaging-documentation)

## Getting Started

- [README.md](./README.md) - The main project overview, providing essential information about Flightctl, its purpose, and basic setup instructions.

## User Documentation

- [docs/user/README.md](./docs/user/README.md) - A landing page for user-focused documentation, offering guidance on installation, configuration, and management of devices and fleets.
- [docs/user/introduction.md](./docs/user/introduction.md) - Introduces the core concepts of Flightctl, explaining how it helps manage devices at scale.
- [docs/user/getting-started.md](./docs/user/getting-started.md) - A step-by-step guide to getting a local Flightctl environment up and running.
- [docs/user/install-cli.md](./docs/user/install-cli.md) - Instructions for installing and configuring the `flightctl` command-line interface.
- [docs/user/building-images.md](./docs/user/building-images.md) - Guidance on creating custom OS images for devices to be managed by Flightctl.
- [docs/user/provisioning-devices.md](./docs/user/provisioning-devices.md) - Explains how to provision and enroll new devices with the Flightctl server.
- [docs/user/managing-devices.md](./docs/user/managing-devices.md) - Covers the lifecycle of device management, including viewing, editing, and deleting devices.
- [docs/user/managing-fleets.md](./docs/user/managing-fleets.md) - Details how to group devices into fleets for easier management and configuration templating.
- [docs/user/configuring-agent.md](./docs/user/configuring-agent.md) - Information on configuring the Flightctl agent on managed devices.
- [docs/user/api-resources.md](./docs/user/api-resources.md) - An overview of the primary API resources available in Flightctl.
- [docs/user/auth-resources.md](./docs/user/auth-resources.md) - Describes the resources used for authentication and authorization.
- [docs/user/kubernetes-auth.md](./docs/user/kubernetes-auth.md) - Explains how to configure authentication using a Kubernetes cluster.
- [docs/user/device-api-statuses.md](./docs/user/device-api-statuses.md) - Details the different status conditions a device can report.
- [docs/user/field-selectors.md](./docs/user/field-selectors.md) - A guide to using field selectors for querying and filtering resources.
- [docs/user/disconnected-cluster.md](./docs/user/disconnected-cluster.md) - Instructions for deploying Flightctl in a disconnected or air-gapped environment.
- [docs/user/registering-microshift-devices-acm.md](./docs/user/registering-microshift-devices-acm.md) - A guide on integrating Flightctl with Red Hat Advanced Cluster Management (ACM) for MicroShift devices.
- [docs/user/service-observability.md](./docs/user/service-observability.md) - Information on monitoring and observing the Flightctl service.
- [docs/user/alerts.md](./docs/user/alerts.md) - Documentation on the alerting system within Flightctl.
- [docs/user/troubleshooting.md](./docs/user/troubleshooting.md) - A guide to troubleshooting common issues with Flightctl.

## Developer Documentation

- [docs/developer/README.md](./docs/developer/README.md) - The main entry point for developer-related documentation, including architecture and enhancement proposals.
- [docs/developer/devicesimulator.md](./docs/developer/devicesimulator.md) - A guide to using the device simulator for testing and development.
- [docs/developer/metrics.md](./docs/developer/metrics.md) - Information on the metrics exposed by Flightctl for monitoring and debugging.

### Architecture

- [docs/developer/architecture/architecture.md](./docs/developer/architecture/architecture.md) - A high-level overview of the Flightctl system architecture.
- [docs/developer/architecture/alerts.md](./docs/developer/architecture/alerts.md) - A detailed description of the alerting architecture.
- [docs/developer/architecture/field-selectors.md](./docs/developer/architecture/field-selectors.md) - The architecture of field selectors for resource queries.
- [docs/developer/architecture/rollout-device-selection.md](./docs/developer/architecture/rollout-device-selection.md) - The design for selecting devices during a fleet rollout.
- [docs/developer/architecture/rollout-disruption-budget.md](./docs/developer/architecture/rollout-disruption-budget.md) - The architecture for managing rollout disruptions.
- [docs/developer/architecture/service-observability.md](./docs/developer/architecture/service-observability.md) - The architecture of the service observability components.
- [docs/developer/architecture/tasks.md](./docs/developer/architecture/tasks.md) - The design of the task management system within Flightctl.

### Enhancements

- [docs/developer/enhancements/fep-000-api-device-fleet.md](./docs/developer/enhancements/fep-000-api-device-fleet.md) - A Flightctl Enhancement Proposal (FEP) for the device and fleet API.
- [docs/developer/enhancements/fep-002-remote-console.md](./docs/developer/enhancements/fep-002-remote-console.md) - A FEP for adding remote console capabilities.

## API Documentation

- [api/v1alpha1/README.md](./api/v1alpha1/README.md) - An overview of the v1alpha1 API, including resource definitions and validation rules.

## Testing Documentation

- [test/README.md](./test/README.md) - An overview of the testing strategy and structure in the Flightctl project.
- [test/harness/e2e/vm/README.md](./test/harness/e2e/vm/README.md) - Information on the end-to-end testing harness for virtual machines.
- [test/scripts/agent-images/README.md](./test/scripts/agent-images/README.md) - Documentation for scripts that build agent images for testing.

## Packaging Documentation

- [packaging/debian/README.md](./packaging/debian/README.md) - Information on how to build Debian packages for Flightctl components.

# CLAUDE.md - Flight Control Documentation Index

## Table of Contents
- [Project Overview](#project-overview)
- [Getting Started](#getting-started)
- [User Documentation](#user-documentation)
- [Developer Documentation](#developer-documentation)
- [API Documentation](#api-documentation)
- [Testing](#testing)
- [Packaging](#packaging)

## Project Overview

**[README.md](./README.md)** - Main project overview, introduction to Flight Control service for declarative management of edge device fleets.

## Getting Started

**[Getting Started](./docs/user/getting-started.md)** - How to get started with Flight Control by deploying the service and enrolling your first device.

**[Introduction](./docs/user/introduction.md)** - An introduction to the project, basic concepts, and Flight Control's high-level architecture.

**[Install CLI](./docs/user/install-cli.md)** - Instructions for installing the Flight Control CLI tool.

## User Documentation

**[User Documentation Index](./docs/user/README.md)** - Main entry point for user documentation with comprehensive navigation.

### Device Management
- **[Managing Devices](./docs/user/managing-devices.md)** - How to manage individual devices in your fleet.
- **[Managing Fleets](./docs/user/managing-fleets.md)** - How to manage fleets of devices collectively.
- **[Provisioning Devices](./docs/user/provisioning-devices.md)** - How to provision devices with OS images.
- **[Device API Statuses](./docs/user/device-api-statuses.md)** - Understanding device status information via API.

### Configuration and Setup
- **[Building Images](./docs/user/building-images.md)** - How to build your own OS images and publish them to a container registry.
- **[Configuring Agent](./docs/user/configuring-agent.md)** - How to configure the Flight Control agent on devices.
- **[Kubernetes Auth](./docs/user/kubernetes-auth.md)** - Authentication configuration for Kubernetes environments.
- **[Auth Resources](./docs/user/auth-resources.md)** - Authentication and authorization resource configuration.

### Advanced Topics
- **[Disconnected Cluster](./docs/user/disconnected-cluster.md)** - Running Flight Control in disconnected/air-gapped environments.
- **[Registering MicroShift Devices with ACM](./docs/user/registering-microshift-devices-acm.md)** - Integration with Advanced Cluster Management.
- **[Field Selectors](./docs/user/field-selectors.md)** - Using field selectors to filter and query resources.
- **[API Resources](./docs/user/api-resources.md)** - Overview of available API resources and their usage.

### Monitoring and Troubleshooting
- **[Alerts](./docs/user/alerts.md)** - Setting up and managing alerts for device fleets.
- **[Service Observability](./docs/user/service-observability.md)** - Monitoring and observability features for Flight Control services.
- **[Troubleshooting](./docs/user/troubleshooting.md)** - Common issues and their solutions.

## Developer Documentation

**[Developer Documentation Index](./docs/developer/README.md)** - Main entry point for developer documentation including building and development setup.

### Architecture
- **[Architecture Overview](./docs/developer/architecture/architecture.md)** - System context and high-level architecture diagrams.
- **[Tasks](./docs/developer/architecture/tasks.md)** - Task management and execution architecture.
- **[Alerts](./docs/developer/architecture/alerts.md)** - Alert system architecture and design.
- **[Service Observability](./docs/developer/architecture/service-observability.md)** - Observability architecture and implementation.
- **[Field Selectors](./docs/developer/architecture/field-selectors.md)** - Field selector implementation and architecture.
- **[Rollout Device Selection](./docs/developer/architecture/rollout-device-selection.md)** - Device selection strategy during rollouts.
- **[Rollout Disruption Budget](./docs/developer/architecture/rollout-disruption-budget.md)** - Managing disruption budgets during rollouts.

### Development Tools
- **[Device Simulator](./docs/developer/devicesimulator.md)** - Tool for simulating devices during development and testing.
- **[Metrics](./docs/developer/metrics.md)** - Metrics collection and monitoring implementation.

### Enhancement Proposals
- **[FEP-000: API Device Fleet](./docs/developer/enhancements/fep-000-api-device-fleet.md)** - Enhancement proposal for API device fleet management.
- **[FEP-002: Remote Console](./docs/developer/enhancements/fep-002-remote-console.md)** - Enhancement proposal for remote console access.

## API Documentation

**[API v1alpha1](./api/v1alpha1/README.md)** - API definition context including device status conditions and core API structures.

## Testing

**[Test Documentation](./test/README.md)** - Main testing documentation and test suite overview.

**[E2E VM Test Harness](./test/harness/e2e/vm/README.md)** - End-to-end testing framework using virtual machines.

**[Agent Images Test Scripts](./test/scripts/agent-images/README.md)** - Test scripts for building and validating agent images.

## Packaging

**[Debian Packaging](./packaging/debian/README.md)** - Debian package building and distribution documentation.

---

*This documentation index is automatically maintained. For the most up-to-date information, please refer to the individual documentation files.*
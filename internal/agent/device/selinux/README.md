# SELinux Policy Management

This package provides SELinux policy management for the FlightCtl agent.

## Purpose

This package addresses SELinux policy loading issues in bootc environments, particularly newer distributions (like EL10) where RPM post-install scripts don't execute properly during image builds, leaving SELinux policies unloaded despite successful package installation.

The PolicyLoader ensures FlightCtl SELinux policies are loaded at agent startup when running on affected systems, providing proper SELinux contexts for agent operation while maintaining compatibility across deployment methods (Helm/Kubernetes and quadlets/systemd).

## Key Features

- Bootc environment detection (like EL10)
- Graceful fallback for systems where policy loading isn't needed
- Runtime policy loading with capability checking
- File context restoration after policy loading
- Non-blocking startup (errors logged but don't stop agent)

## Deployment Compatibility

The implementation is deployment-agnostic and works consistently across:

- Helm/Kubernetes deployments (container orchestration)
- Quadlets/systemd deployments (host system services)
- Traditional RPM-based systems (where policies load normally)
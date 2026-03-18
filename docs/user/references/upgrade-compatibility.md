# Upgrade Compatibility Matrix

This document describes the supported upgrade paths for Flight Control.

## Overview

Flight Control follows semantic versioning. When upgrading Flight Control, you must follow supported upgrade paths to ensure data integrity and service continuity.

> [!IMPORTANT]
> There are no current upgrade paths. This document will be updated with supported upgrade paths as they become available.

## Supported Upgrade Paths

| From Version | To Version | Notes |
|--------------|------------|-------|
| v1.0.0 | — | No current upgrade paths |

## Before You Upgrade

Before attempting an upgrade:

1. **Back up your database** - Follow the [database backup procedure](../installing/performing-database-backup.md) before any upgrade
2. **Review release notes** - Check the release notes for any breaking changes or migration steps
3. **Test in a non-production environment** - Validate the upgrade in a staging environment first

## Upgrade Procedure

Detailed upgrade procedures will be documented here once upgrade paths are available.

## Upgrade Notes

### Upgrading from 1.0 to 1.1

#### UI TLS secret rename (multicluster engine plugin only)

When the UI is deployed as a plugin to multicluster engine, the TLS secret was
renamed from `flightctl-ui-serving-cert` to `flightctl-ui-server-tls` to align
with the certificate generation mechanism used by other Flight Control services.

After upgrading, the old `flightctl-ui-serving-cert` secret is orphaned. Once
the UI pod is running successfully, you can safely delete it:

```bash
kubectl delete secret flightctl-ui-serving-cert -n <namespace>
```

## API Version Compatibility

Flight Control maintains API version compatibility according to these principles.

### Stability Levels

| Level | Description | Support Guarantee |
|-------|-------------|-------------------|
| **v1alphaX** | Alpha versions | TBD |
| **v1betaX** | Beta versions | Supported throughout the 1.x.x major version |
| **v1** | Stable version | TBD |

### Client Recommendations

1. **Always specify a version**: Use the `Flightctl-API-Version` header to ensure consistent behavior across server upgrades

2. **Monitor deprecation headers**: Check responses for the `Deprecation` header and plan migrations accordingly

3. **Handle version negotiation failures**: If you receive HTTP 406 Not Acceptable, check the `Flightctl-API-Versions-Supported` header for available versions

### Server Upgrade Impact

When upgrading the Flight Control server:

- Existing API versions continue to work unless explicitly removed
- New versions may be added without affecting existing clients
- For versioned resources, the server returns the `Flightctl-API-Version` header indicating which version was used

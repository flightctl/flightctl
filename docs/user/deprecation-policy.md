# Flight Control Deprecation Policy

Flight Control follows a deprecation policy to ensure smooth upgrades and backward compatibility.

## Overview

This document describes how database schema elements are deprecated and removed. Understanding this policy helps you plan upgrades and avoid disruptions.

## Database Schema Changes

Flight Control follows these guidelines when making breaking changes to the database schema:

### Deprecation Timeline

1. **Deprecation Declaration**: Schema elements marked for removal are declared as deprecated in the release notes
2. **Minimum Grace Period**: Deprecated schema elements remain in the database for at least **one minor version** before removal
3. **Usage Logging**: Access to deprecated schema elements is logged as warnings to help identify dependencies

#### Example Timeline

- **Version 1.0.0**: Schema element `example_field` is declared deprecated in release notes
- **Version 1.1.0**: Element remains present but deprecated (warnings logged on use)
- **Version 1.2.0**: Element can be safely removed (earliest allowed removal)

### Migration Strategy

Database schema changes follow an expand-contract pattern:

1. **Expand Phase**: New schema elements are added while retaining deprecated ones
2. **Transition Period**: Both old and new schema elements coexist (minimum one minor version)
3. **Contract Phase**: Deprecated schema elements are removed after the grace period

### User Responsibilities

- **Review release notes** for each version to identify deprecated schema elements and required actions
- Monitor deprecation warnings in logs
- Update applications to use new schema elements during the transition period
- Ensure applications are compatible before upgrading past the removal version

## Versioning

Flight Control follows semantic versioning (MAJOR.MINOR.PATCH):

> [!NOTE]
> Deprecated schema elements remain functional for at least one minor version to give users time to migrate.

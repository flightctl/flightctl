# PostgreSQL Backup Strategy

This document provides guidance on PostgreSQL backup strategies for Flight Control, including Recovery Point Objective (RPO) considerations and customer responsibilities for managing database backups.

## Overview

Flight Control relies on PostgreSQL as its primary database for storing all critical data including device configurations, fleet management data, user information, and system state. Proper backup strategies are essential to ensure data protection, disaster recovery, and business continuity.

**Recovery Point Objective (RPO)**: Customers must determine their own RPO based on business requirements, regulatory compliance, and risk tolerance. Typical RPOs range from 15 minutes for mission-critical environments to 24 hours for development environments.

⚠️ **Important**: Customers are responsible for implementing and managing their own backup strategies. Flight Control provides guidance and recommendations but does not manage backups for customer deployments.

## What Needs to be Backed Up

**The `flightctl` table in the PostgreSQL database** must be backed up, including:

- All data tables (devices, fleets, organizations, templates, etc.)
- Database schema and structure
- Database users and permissions

## Testing Backups

Regular testing of backup and restore procedures is critical to ensure data recovery capabilities.

### Test Procedures

1. Create test environment
2. Restore from most recent backup
3. Verify data integrity by listing all Flight Control resources and comparing to production
4. Test application functionality
5. Document any issues and resolutions

## Best Practices

### GitOps and Deployment Configuration

1. **Use GitOps with Repository Resources**: Store all Flight Control configurations in a Git repository and use the `Repository` resource to reference Git configurations via `gitRef` in device and fleet specifications (see [API Resources](api-resources.md#repositories) and [Managing Devices](managing-devices.md#getting-configuration-from-a-git-repository))
2. **Separate Git Server**: Use external Git hosting services (GitHub, GitLab, Bitbucket) in a different failure domain than the main Flight Control infrastructure
3. **Backup Deployment Configs**: Regularly backup Helm values files and configuration files
4. **Version Control**: Tag and version all configuration changes for easy rollback

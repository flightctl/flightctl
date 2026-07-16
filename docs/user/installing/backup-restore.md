# Backup and restore

This document describes how to back up and restore Flight Control server state using the `flightctl-backup` and `flightctl-restore` CLI commands.

## Overview

Flight Control provides CLI commands for backing up and restoring server state (database, PKI materials, encryption keys, and service configuration), enabling disaster recovery without device re-enrollment.

Supports Podman and Kubernetes with Helm deployments.
Works with cron or Kubernetes CronJob schedulers.

⚠️ **Warning**: Backup archives contain sensitive materials including database content, CA private keys, TLS certificates, encryption keys, and database credentials. Store backup archives on encrypted storage with restricted access and use encrypted channels when transporting archives.

**Key capabilities:**

- Back up database, PKI materials, encryption keys, and service configuration to timestamped archives
- Restore from backup archives with automatic integrity verification
- Podman and Kubernetes with Helm deployments
- Cron and Kubernetes CronJob automation

**Limitations:**

- **Same-version restore only** — a backup from version 1.1 cannot restore to version 1.2
- **Deployment-type specific** — Podman backup can only restore to Podman; Kubernetes backup can only restore to Kubernetes
- **External databases** — if Flight Control uses an external PostgreSQL database, you must back up the database separately using your organization's standard procedures

**Migration from legacy manual restore:** This archive-based backup and restore workflow replaces the previous manual database backup and restore procedures. The new `flightctl-backup` and `flightctl-restore` commands provide a unified approach that includes database, PKI materials, and service configuration.

## Prerequisites

**All deployments:**

- `flightctl-backup` and `flightctl-restore` commands — included in the Flight Control CLI download (same package as `flightctl`)
- PostgreSQL client tools — must be installed separately:
  - RHEL/Fedora: `dnf install postgresql`
  - Debian/Ubuntu: `apt install postgresql-client`
  - Required: `pg_dump` (for backup), `psql` (for restore)

**Kubernetes deployments:**

- `kubectl` configured and authenticated to the cluster where Flight Control is deployed
- `helm` CLI (for Helm values backup, optional)

**Podman deployments:**

- `podman` CLI (for volume backup and restore)
- Root or sudo access (for accessing `/etc/flightctl/`)

**Important**: Ensure you have sufficient disk space for backup archives. Archive size depends on database size (typically 50% of uncompressed database size for small to medium deployments).

## Backup

### Basic usage

Use the `flightctl-backup` command with the `--output` flag to specify where backup archives should be written (default: current directory).

**Podman:**

```bash
sudo flightctl-backup --output /var/backups/flightctl/
```

**Kubernetes:**

```bash
flightctl-backup --output /mnt/backups/
```

The backup command creates two files in the specified output directory:

- `flightctl-backup-<timestamp>.tar.gz` — the backup archive (e.g., `flightctl-backup-20260428T120000Z.tar.gz`)
- `flightctl-backup-<timestamp>.tar.gz.sha256` — checksum

### Archive contents

Backup archives contain the following:

- `db/dump.sql` — PostgreSQL database dump (internal database only; omitted for external databases)
- `pki/` — CA keys and TLS certificates (Podman: directory tree, Kubernetes: Secret YAML exports)
- `encryption/` — Encryption keys (Podman: key files, Kubernetes: Secret YAML export)
- `config/` — Service configuration files
- `volumes/` — Podman volumes (PAM Issuer user database, if present)
- `metadata.json` — Backup metadata (timestamp, Flight Control version, deployment type)

### External database backup

If Flight Control uses an external PostgreSQL database, the backup command detects this configuration and skips database backup:

```text
External database detected. Please back up the database separately.
```

You are responsible for backing up the external database using your organization's backup procedures. Coordinate external database backups with Flight Control backups to ensure consistency.

**Example PostgreSQL backup commands:**

```bash
# Using pg_dump (logical backup)
pg_dump -h postgres.example.com -U flightctl_app -d flightctl -F c -f flightctl-db-backup.dump

# Using pg_basebackup (physical backup)
pg_basebackup -h postgres.example.com -U replication_user -D /backup/postgres -Fp -Xs -P
```

**Important**: Ensure your external database backup includes:

- All Flight Control tables and data
- Database schema and structure
- Database users and permissions

Consult your database administrator or cloud provider documentation for recommended backup procedures for your environment.

### Scheduled backups

You can integrate `flightctl-backup` with external schedulers to automate regular backups.

**Podman (cron):**

```bash
# Add to root crontab (sudo crontab -e): daily backup at 2 AM
0 2 * * * /usr/bin/flightctl-backup --output /var/backups/flightctl/
```

Note: The `flightctl-backup` command requires root privileges to access Podman containers and write to `/var/backups/flightctl/`. Install this cron entry in the root crontab using `sudo crontab -e`.

**Kubernetes (CronJob):**

**Important**: Use the same Flight Control version for the backup image as your deployed server version to ensure backup/restore compatibility.

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: flightctl-backup
  namespace: flightctl-internal
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: backup
            image: quay.io/flightctl/flightctl:<version>  # Replace <version> with your deployed Flight Control version
            command: ["/usr/bin/flightctl-backup", "--output", "/backups"]
            volumeMounts:
            - name: backup-storage
              mountPath: /backups
          volumes:
          - name: backup-storage
            persistentVolumeClaim:
              claimName: flightctl-backups
          restartPolicy: OnFailure
```

## Restore

### Prerequisites

Before restoring from a backup:

1. Ensure the backup archive and checksum file are available
2. Verify sufficient disk space for archive extraction

**Important**: The restore operation requires that the backup was created on the same deployment type (Podman or Kubernetes) and the same Flight Control version. Attempting to restore a Podman backup on Kubernetes, or vice versa, will fail with a deployment type mismatch error.

**Note**: The `flightctl-restore` command automatically stops Flight Control services before restore and starts them after restore completes. Manual service management is not required.

### Basic usage

```bash
flightctl-restore /path/to/flightctl-backup-<timestamp>.tar.gz
```

The restore process performs the following steps:

1. Validates the archive checksum
2. Extracts the archive to a temporary directory
3. Stops Flight Control services
4. Restores the database (if `db/dump.sql` is present in the archive)
5. Restores PKI materials
6. Restores encryption keys (if `encryption/` is present in the archive)
7. Restores service configuration
8. Prepares devices (clears KV store, updates device annotations)
9. Starts Flight Control services

**Note**: Services are automatically started even if the restore fails, ensuring the system is not left in a stopped state.

### External database restore

If the backup archive does not contain `db/dump.sql` (external database), `flightctl-restore` pauses and displays instructions:

```text
External database backup detected.
Please restore the database using your backup procedures, then press Enter to continue.
```

Restore your external database before continuing:

**Example PostgreSQL restore commands:**

```bash
# Using pg_restore (from pg_dump custom format)
pg_restore -h postgres.example.com -U flightctl_app -d flightctl -c flightctl-db-backup.dump

# Using psql (from SQL dump)
psql -h postgres.example.com -U flightctl_app -d flightctl -f flightctl-db-backup.sql

# Using pg_basebackup restore (physical backup)
# Stop PostgreSQL, replace data directory, restart PostgreSQL
```

After the external database is restored, press Enter in the `flightctl-restore` terminal to continue. The restore process will then restore PKI materials, configuration, and prepare devices.

**Important**: Ensure the external database is fully restored and accessible before continuing. The restore process validates database connectivity before proceeding.

### Post-restore verification

After the restore operation completes, verify the restore:

- Devices reconnect without re-enrollment
- Fleet configurations are intact
- API responds to queries

## Post-restore device status changes

After completing the restore operation, devices will undergo automatic status transitions based on their state relative to the restored data. Understanding these status changes is crucial for proper post-restore management.

### Device status transitions

#### 1. AwaitingReconnect status

All devices will initially be moved to `AwaitingReconnect` status after the restore operation completes. This indicates that:

- The Flight Control service is waiting for devices to reconnect and report their current state
- Spec rendering is temporarily stopped for these devices
- No configuration changes will be applied until the device reconnects

**What to expect:**

- Devices will remain in this status until they successfully reconnect to the Flight Control service
- Once reconnected, the system will evaluate the device's current state against the restored specifications

#### 2. ConflictPaused status

If a device's specification in the restored backup is determined to be older than the device's current reported state, the device will be moved to `ConflictPaused` status. This indicates:

- A potential conflict between the restored specification and the device's actual state
- Spec rendering is stopped to prevent unintended configuration changes
- **Human intervention is required** to resolve the conflict

**What to expect:**

- The device will not receive any configuration updates while in this status
- Manual review and action are needed to determine the correct course of action
- The device will remain in `ConflictPaused` until explicitly resumed

#### 3. Normal operation status

If the device's current state is compatible with the restored specification, the device will return to normal operational status (e.g., `Online`, `Updating`, etc.).

**What to expect:**

- Normal spec rendering and configuration management resume
- The device continues normal operation with the restored configuration

### Managing post-restore device states

#### Monitoring device status

After the restore operation, monitor device statuses to identify which devices require attention:

```bash
# Check all device statuses
flightctl get dev

# Filter devices in specific states
flightctl get dev --field-selector=status.summary.status=AwaitingReconnect
flightctl get dev --field-selector=status.summary.status=ConflictPaused
```

#### Resolving ConflictPaused devices

For devices in `ConflictPaused` status, you have several options:

1. **Review and update the device specification**: if the device is owned by a fleet, review the relevant fleet spec (including template and selector). If the device isn't owned by a fleet, review the device's spec. In either case, review the device's labels and owner.
2. **Resume the device(s)** if you're confident the restored specification is correct, resume the device in any of the following ways:

```bash
# Resume a specific device by name
flightctl resume device <device-name>

# Resume devices using label selectors
flightctl resume device --selector="environment=production"
flightctl resume device --selector="fleet=web-servers,region=us-east"

# Resume devices using field selectors
flightctl resume device --field-selector="text"

# Combine label and field selectors
flightctl resume device --selector="environment=production" --field-selector="text"
```

## Troubleshooting

### Backup errors

**Error:** `database connection failed`

- **Cause:** Database is not running or connection credentials are incorrect
- **Solution:** Verify that the database is running and check the service configuration for correct credentials

**Error:** `disk full`

- **Cause:** Insufficient disk space for the backup archive
- **Solution:** Free disk space on the output directory or specify a different `--output` directory with more available space

### Restore errors

**Error:** `checksum mismatch`

- **Cause:** Archive is corrupted or has been modified
- **Solution:** Use a different backup archive or re-create the backup

**Error:** `deployment type mismatch: archive is podman, current environment is kubernetes`

- **Cause:** Attempting to restore a Podman backup on a Kubernetes deployment
- **Solution:** Restore the backup on the same deployment type that created it (Podman backup → Podman restore, Kubernetes backup → Kubernetes restore)

**Error:** `psql: command not found`

- **Cause:** PostgreSQL client tools are not installed
- **Solution:** Install the PostgreSQL client package for your operating system

## Best practices

1. **Regular backups:** Schedule daily backups during low-traffic periods (e.g., 2 AM) to minimize impact on production workloads
2. **Backup retention:** Keep at least 7 days of backups to enable recovery from recent incidents
3. **Secure storage:** Store backup archives on encrypted storage with restricted access. Limit access to archives to authorized administrators only.
4. **Test restores:** Periodically test the restore procedure in a non-production environment to ensure backups are valid and the restore process is well understood
5. **Documentation:** Maintain detailed restore procedures including step-by-step recovery steps, required preconditions, rollback actions, responsible roles, and contact information for support escalation
6. **Monitoring:** Check archive integrity in logs
   - Podman: `journalctl -u flightctl-backup` or cron logs (`/var/log/cron`)
   - Kubernetes: `kubectl logs -n <namespace> <backup-pod>` for the backup CronJob

# Bug Report: flightctl-restore fails with "connection refused" during post-restoration

## Summary
The `flightctl-restore` command fails when attempting to connect to the database for post-restoration device preparation in Podman/Quadlet deployments, with error:
```
failed to connect database: failed to connect to `host=localhost user=flightctl_app database=flightctl`: 
dial error (dial tcp [::1]:5432: connect: connection refused)
```

## Environment
- **Deployment Type**: Podman (Quadlet)
- **Test Suite**: `backup_restore/backup_restore_test.go`
- **Affected Version**: v1.3.0-main-67-gff40fbdf

## Steps to Reproduce

1. Set up a Quadlet/Podman deployment of FlightCtl
2. Create some test resources (devices, fleets, enrollment requests)
3. Run `flightctl-backup --output /tmp/backup --deployment-type podman`
   - Backup completes successfully, creates `.tar.gz` and `.sha256` files
4. Run `flightctl-restore /tmp/backup/flightctl-backup-*.tar.gz`
5. Observe the failure during post-restoration step

## Expected Behavior
The restore should:
1. Stop FlightCtl services (api, worker, periodic, etc.)
2. Restore database dump
3. Restore PKI materials
4. Restore service configuration
5. **Start database and KV services**
6. Connect to database for post-restoration device preparation
7. Start all FlightCtl services

## Actual Behavior
The restore process:
1. ✅ Stops FlightCtl services
2. ✅ Restores database dump successfully
3. ✅ Restores PKI materials
4. ✅ Restores service configuration
5. ❌ Attempts to connect to database without starting it first
6. ❌ Fails with "connection refused"

### Error Log
```
time="2026-06-08T14:09:46-04:00" level=info msg="Service configuration restore completed"
time="2026-06-08T14:09:46-04:00" level=info msg="Retrieving service credentials from infrastructure"
time="2026-06-08T14:09:46-04:00" level=info msg="Exposing database for post-restoration device preparation"
time="2026-06-08T14:09:46-04:00" level=info msg="Exposing KV store for post-restoration device preparation"
time="2026-06-08T14:09:46-04:00" level=info msg="/home/kni/flightctl/internal/store/gorm.go:67\n[error] failed to initialize database, got error failed to connect to `host=localhost user=flightctl_app database=flightctl`: dial error (dial tcp [::1]:5432: connect: connection refused)"
time="2026-06-08T14:09:46-04:00" level=fatal msg="failed to connect database: failed to connect to `host=localhost user=flightctl_app database=flightctl`: dial error (dial tcp [::1]:5432: connect: connection refused)"
```

## Root Cause Analysis

### Code Flow
The restore workflow in `internal/restore/restore.go`:

```go
// Line 94-97: Stop all services (api, worker, periodic, etc.)
// Note: Does NOT stop flightctl-db or flightctl-kv - they're intentionally excluded
if err := deployer.StopServices(ctx); err != nil {
    return fmt.Errorf("failed to stop FlightCtl services: %w", err)
}

// Lines 99-115: Restore database, PKI, config
if err := deployer.RestoreDatabase(ctx, extractDir); err != nil {
    return fmt.Errorf("database restore failed: %w", err)
}
// ... PKI and config restore ...

// Line 123-127: Try to expose database
// PROBLEM: Database service may not be running at this point
dbHost, dbPort, dbCleanup, err := deployer.ExposeService(ctx, "flightctl-db")

// Line 143: Try to connect to database - FAILS
db, err := store.InitDB(&exposedCfg, log)
```

### Why It Fails
1. `StopServices()` stops only services with DB connections (api, worker, etc.)
2. In theory, `flightctl-db.service` and `flightctl-kv.service` should remain running
3. However, in practice, the database service is not accessible when `ExposeService()` is called
4. Possible reasons:
   - Database service was stopped as a dependency of other services
   - Database container takes time to start and isn't ready yet
   - There's no explicit guarantee that database service is running and ready

### Missing Step
The restore workflow assumes database and KV services are running after configuration restore, but **never explicitly starts or verifies them** before attempting to connect.

## Solution

Add a new `StartInfraServices()` method to the `Deployer` interface that explicitly ensures database and KV services are running before attempting to connect.

### Implementation

**1. Add interface method** (`internal/restore/deployer.go`):
```go
type Deployer interface {
    // ... existing methods ...
    
    // StartInfraServices ensures database and KV services are running.
    // Must be called before ExposeService to ensure infrastructure is accessible.
    StartInfraServices(ctx context.Context) error
}
```

**2. Implement for Podman** (`internal/restore/deployer.go`):
```go
func (p *PodmanRestoreDeployer) StartInfraServices(ctx context.Context) error {
    infraServices := []string{"flightctl-db.service", "flightctl-kv.service"}
    args := append([]string{"start"}, infraServices...)
    if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
        return fmt.Errorf("systemctl start infra services failed: %w (output: %s)", err, out)
    }
    p.log.Info("Infrastructure services started")
    return nil
}
```

**3. Implement for Kubernetes** (`internal/restore/deployer.go`):
```go
func (k *KubernetesRestoreDeployer) StartInfraServices(ctx context.Context) error {
    k.log.Info("Infrastructure services (database/KV) remain running in Kubernetes")
    return nil // No-op for Kubernetes - StatefulSets remain running
}
```

**4. Call before ExposeService** (`internal/restore/restore.go`):
```go
log.Info("Service configuration restore completed")

// NEW: Start infrastructure services
log.Info("Starting database and KV services for post-restoration")
if err := deployer.StartInfraServices(ctx); err != nil {
    return fmt.Errorf("failed to start infrastructure services: %w", err)
}

log.Info("Retrieving service credentials from infrastructure")
cfg, err := deployer.GetConfig(ctx)
// ... rest of post-restoration flow ...
```

## Testing

### Before Fix
```bash
$ flightctl-restore /tmp/backup.tar.gz
# ... restore progress ...
time="..." level=fatal msg="failed to connect database: ... connection refused"
# EXIT CODE: 1
```

### After Fix
```bash
$ flightctl-restore /tmp/backup.tar.gz
# ... restore progress ...
time="..." level=info msg="Service configuration restore completed"
time="..." level=info msg="Starting database and KV services for post-restoration"
time="..." level=info msg="Infrastructure services started"
time="..." level=info msg="Exposing database for post-restoration device preparation"
time="..." level=info msg="Running post-restoration device preparation"
# ... success ...
# EXIT CODE: 0
```

## Impact

**Severity**: High
- Backup/restore is a critical feature for production deployments
- Users cannot successfully restore from backups in Podman/Quadlet environments
- Affects disaster recovery scenarios

**Workaround**: None (the restore binary needs to be fixed)

## Related Issues
- EDM-4083: Run backup inside VM for quadlet e2e tests (merged PR #3058)
- EDM-3697: Fix binary copy in E2E tests (this PR)

## Files Changed
- `internal/restore/restore.go` - Add StartInfraServices call
- `internal/restore/deployer.go` - Add interface method and implementations
- `internal/restore/mock_deployer.go` - Add mock for tests

## Verification
Run backup/restore e2e test:
```bash
make test-e2e GINKGO_FOCUS="backup.*restore"
```

# FlightCtl Backup and Restore Operations

This document provides step-by-step instructions for backing up and restoring FlightCtl data, including running the `flightctl-restore` command to restore FlightCtl data from backups.

## Overview

The `flightctl-restore` command is used to prepare devices and data after restoring FlightCtl from a backup. This process requires temporarily stopping FlightCtl services, accessing the database and KV store directly, and then restarting services.

⚠️ **Important**: This operation should only be performed during maintenance windows as it requires stopping all FlightCtl services.

## Prerequisites

- Access to the Kubernetes cluster where FlightCtl is deployed
- `kubectl` configured with appropriate permissions
- FlightCtl CLI tools available
- Database backup files available for restoration
- redis-cli - optional, for verification only
- pg_isready - optional, for verification only

## Step-by-Step Restore Process

### Step 1: Stop FlightCtl Services

Scale down all FlightCtl service deployments to prevent data conflicts during the restore process:

```bash
# Scale down API service
kubectl scale deployment flightctl-api --replicas=0 -n flightctl-external

# Scale down Worker service  
kubectl scale deployment flightctl-worker --replicas=0 -n flightctl-internal

# Scale down Periodic service
kubectl scale deployment flightctl-periodic --replicas=0 -n flightctl-internal

# Scale down Alert Exporter service
kubectl scale deployment flightctl-alert-exporter --replicas=0 -n flightctl-internal

# Scale down Alertmanager Proxy service
kubectl scale deployment flightctl-alertmanager-proxy --replicas=0 -n flightctl-external

# Verify all services are scaled down
kubectl get deployments -n flightctl-external
kubectl get deployments -n flightctl-internal
```

Wait for all pods to terminate before proceeding:

```bash
kubectl get pods -n flightctl-external
kubectl get pods -n flightctl-internal
```

### Step 2: Restore Database from Backup

Now that all FlightCtl services are stopped, restore your database from backup using your preferred restoration method.

**Database Restoration:**

- Restore the PostgreSQL database (`flightctl`) from your backup
- You can use any method you prefer (pg_restore, psql with SQL dumps, volume snapshots, etc.)
- Ensure the database is accessible and contains your backed-up data

⚠️ **Note**: The specific restoration commands depend on your backup strategy and tools. Ensure the database is fully restored before proceeding to the next step.

### Step 3: Retrieve Database and KV Store Credentials

#### Database Credentials

```bash
# Get application database password
DB_APP_PASSWORD=$(kubectl get secret flightctl-db-app-secret -n flightctl-internal -o jsonpath='{.data.userPassword}' | base64 -d)

echo "Database password retrieved successfully"
```

#### KV Store (Redis) Credentials

```bash
# Get KV store password
KV_PASSWORD=$(kubectl get secret flightctl-kv-secret -n flightctl-internal -o jsonpath='{.data.password}' | base64 -d)

echo "KV Password retrieved successfully"
```

### Step 4: Set Up Port Forwarding

Open separate terminal sessions for each port forward, or run them in the background:

#### Database Port Forward

```bash
# Forward database port (run in separate terminal or background)
kubectl port-forward svc/flightctl-db 5432:5432 -n flightctl-internal &
DB_PORT_FORWARD_PID=$!

# Verify database connectivity ( if available)
pg_isready -h localhost -p 5432
```

#### KV Store Port Forward

```bash
# Forward KV store port (run in separate terminal or background)  
kubectl port-forward svc/flightctl-kv 6379:6379 -n flightctl-internal &
KV_PORT_FORWARD_PID=$!

# Verify KV store connectivity (if available)
REDISCLI_AUTH="$KV_PASSWORD" redis-cli -h localhost -p 6379 ping
```

### Step 5: Run the Restore Command

Execute the flightctl-restore command using environment variables for database and KV store passwords:

```bash
# Run the restore command with environment variables
DB_PASSWORD="$DB_APP_PASSWORD" KV_PASSWORD="$KV_PASSWORD" ./bin/flightctl-restore
```

Monitor the restore process output for any errors or completion messages.

### Step 6: Clean Up Port Forwards

After the restore command completes successfully:

```bash
# Kill port forward processes
kill $DB_PORT_FORWARD_PID $KV_PORT_FORWARD_PID

# Or if running in separate terminals, use Ctrl+C to stop them
```

### Step 7: Restart FlightCtl Services

Scale the services back up to their normal replica counts:

```bash
# Scale up API service
kubectl scale deployment flightctl-api --replicas=1 -n flightctl-external

# Scale up Worker service
kubectl scale deployment flightctl-worker --replicas=1 -n flightctl-internal

# Scale up Periodic service  
kubectl scale deployment flightctl-periodic --replicas=1 -n flightctl-internal

# Scale up Alert Exporter service
kubectl scale deployment flightctl-alert-exporter --replicas=1 -n flightctl-internal

# Scale up Alertmanager Proxy service
kubectl scale deployment flightctl-alertmanager-proxy --replicas=1 -n flightctl-external

# Verify all services are running
kubectl get deployments -n flightctl-external
kubectl get deployments -n flightctl-internal

# Check pod status
kubectl get pods -n flightctl-external
kubectl get pods -n flightctl-internal
```

## Post-Restore Device Status Changes

After completing the restore operation, devices will undergo automatic status transitions based on their state relative to the restored data. Understanding these status changes is crucial for proper post-restore management.

### Device Status Transitions

#### 1. AwaitingReconnect Status

All devices will initially be moved to `AwaitingReconnect` status after the restore operation completes. This indicates that:

- The FlightCtl service is waiting for devices to reconnect and report their current state
- Spec rendering is temporarily stopped for these devices
- No configuration changes will be applied until the device reconnects

**What to expect:**

- Devices will remain in this status until they successfully reconnect to the FlightCtl service
- Once reconnected, the system will evaluate the device's current state against the restored specifications

#### 2. ConflictPaused Status

If a device's specification in the restored backup is determined to be newer than the device's current reported state, the device will be moved to `ConflictPaused` status. This indicates:

- A potential conflict between the restored specification and the device's actual state
- Spec rendering is stopped to prevent unintended configuration changes
- **Human intervention is required** to resolve the conflict

**What to expect:**

- The device will not receive any configuration updates while in this status
- Manual review and action are needed to determine the correct course of action
- The device will remain in `ConflictPaused` until explicitly resumed

#### 3. Normal Operation Status

If the device's current state is compatible with the restored specification, the device will return to normal operational status (e.g., `Online`, `Updating`, etc.).

**What to expect:**

- Normal spec rendering and configuration management resume
- The device continues normal operation with the restored configuration

### Managing Post-Restore Device States

#### Monitoring Device Status

After the restore operation, monitor device statuses to identify which devices require attention:

```bash
# Check all device statuses
flightctl get dev

# Filter devices in specific states
flightctl get dev --field-selector=status.summary.status=AwaitingReconnect
flightctl get dev --field-selector=status.summary.status=ConflictPaused
```

#### Resolving ConflictPaused Devices

For devices in `ConflictPaused` status, you have several options:

1. **Review and update the device specification** , if the device is owned by a fleet , review the relevant's fleet specs
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
  
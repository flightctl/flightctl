# Flight Control Restore Operations

This document provides step-by-step instructions for restoring Flight Control data from backups, including running the `flightctl-restore` command to restore Flight Control data from backups.

## Overview

The `flightctl-restore` command is used to prepare devices and data after restoring Flight Control from a backup. This process requires temporarily stopping Flight Control services, accessing the database and KV store directly, and then restarting services.

⚠️ **Important**: This operation should only be performed during maintenance windows as it requires stopping all Flight Control services.

## Prerequisites

### For Kubernetes Deployment

- Access to the Kubernetes cluster where Flight Control is deployed
- `kubectl` configured with appropriate permissions
- Flight Control CLI tools available
- Database backup files available for restoration
- redis-cli - optional, for verification only
- pg_isready - optional, for verification only

### For Quadlets Deployment

- Root access to the host system where Flight Control quadlets are deployed
- `systemctl` and `podman` available
- Flight Control CLI tools available
- Database backup files available for restoration
- redis-cli - optional, for verification only
- pg_isready - optional, for verification only

## Step-by-Step Restore Process

This document covers restore procedures for both Kubernetes and Quadlets deployments. Choose the appropriate section based on your deployment method:

- **Kubernetes Deployment**: Flight Control services running as Kubernetes deployments, managed with `kubectl`
- **Quadlets Deployment**: Flight Control services running as systemd-managed containers using Podman Quadlets, managed with `systemctl`

## Kubernetes Deployment Restore Process

### Step 1: Verify Version Compatibility

Verify that the `flightctl-restore` version matches the Flight Control server version to ensure compatibility:

```bash
# Get the Flight Control server version
flightctl version

# Get the flightctl-restore version
flightctl-restore version

# Compare the versions - they should match
echo "Server version: $(flightctl version)"
echo "Restore version: $(flightctl-restore version)"
```

⚠️ **Important**: The versions must match to ensure compatibility. If they don't match, update your `flightctl-restore` binary to match the server version before proceeding.

### Step 2: Stop Flight Control Services

Scale down all Flight Control service deployments to prevent data conflicts during the restore process:

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

### Step 3: Restore Database from Backup

Now that all Flight Control services are stopped, restore your database from backup using your preferred restoration method.

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

### Step 5: Set Up Port Forwarding

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

### Step 6: Run the Restore Command

Execute the flightctl-restore command using environment variables for database and KV store passwords:

```bash
# Run the restore command with environment variables
DB_PASSWORD="$DB_APP_PASSWORD" KV_PASSWORD="$KV_PASSWORD" ./bin/flightctl-restore
```

Monitor the restore process output for any errors or completion messages.

### Step 7: Clean Up Port Forwards

After the restore command completes successfully:

```bash
# Kill port forward processes
kill $DB_PORT_FORWARD_PID $KV_PORT_FORWARD_PID

# Or if running in separate terminals, use Ctrl+C to stop them
```

### Step 8: Restart Flight Control Services

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

## Quadlets Deployment Restore Process

### Step 1: Verify Version Compatibility

Verify that the `flightctl-restore` version matches the Flight Control server version to ensure compatibility:

```bash
# Get the Flight Control server version
flightctl version

# Get the flightctl-restore version
flightctl-restore version

# Compare the versions - they should match
echo "Server version: $(flightctl version)"
echo "Restore version: $(flightctl-restore version)"
```

⚠️ **Important**: The versions must match to ensure compatibility. If they don't match, update your `flightctl-restore` binary to match the server version before proceeding.

### Step 2: Stop Flight Control Services

Stop the Flight Control application services to prevent data conflicts during the restore process:

```bash
# Stop only the application services (keep database and KV store running)
sudo systemctl stop flightctl-api.service
sudo systemctl stop flightctl-worker.service
sudo systemctl stop flightctl-periodic.service
sudo systemctl stop flightctl-alert-exporter.service
sudo systemctl stop flightctl-alertmanager-proxy.service

# Verify application services are stopped
sudo systemctl status flightctl-api.service
sudo systemctl status flightctl-worker.service
sudo systemctl status flightctl-periodic.service
sudo systemctl status flightctl-alert-exporter.service
sudo systemctl status flightctl-alertmanager-proxy.service
```

Wait for all services to stop before proceeding:

```bash
# Monitor service status until all are inactive
watch "sudo systemctl is-active flightctl-api.service flightctl-worker.service flightctl-periodic.service flightctl-alert-exporter.service flightctl-alertmanager-proxy.service"
```

### Step 3: Restore Database from Backup

Now that all Flight Control services are stopped, restore your database from backup using your preferred restoration method.

**Database Restoration:**

- Restore the PostgreSQL database (`flightctl`) from your backup
- You can use any method you prefer (pg_restore, psql with SQL dumps, volume snapshots, etc.)
- Ensure the database is accessible and contains your backed-up data

⚠️ **Note**: The specific restoration commands depend on your backup strategy and tools. Ensure the database is fully restored before proceeding to the next step.

### Step 4: Retrieve Database and KV Store Credentials

#### Database Credentials

```bash
# Get application database password from podman secrets
DB_APP_PASSWORD=$(sudo podman secret inspect flightctl-postgresql-user-password --showsecret | jq -r '.[0].SecretData')

echo "Database password retrieved successfully"
```

#### KV Store (Redis) Credentials

```bash
# Get KV store password from podman secrets
KV_PASSWORD=$(sudo podman secret inspect flightctl-kv-password --showsecret | jq -r '.[0].SecretData')

echo "KV Password retrieved successfully"
```

### Step 5: Access Database and KV Store

In quadlets deployment, you can access the database and KV store directly through the running containers:

#### Database Access

```bash
# Verify database connectivity
sudo podman exec flightctl-db pg_isready -U postgres

# Connect to database (if needed for verification)
sudo podman exec -it flightctl-db psql -U flightctl_app -d flightctl
```

#### KV Store Access

```bash
# Verify KV store connectivity
sudo podman exec flightctl-kv redis-cli ping

# Connect to KV store (if needed for verification)
sudo podman exec -it flightctl-kv redis-cli
```

### Step 6: Set Up Port Forwarding

The database and KV store containers run on a private network and are not accessible from localhost. Use systemd drop-in files to temporarily enable port forwarding:

```bash
# Get the container file locations dynamically
DB_CONTAINER_FILE=$(systemctl show flightctl-db.service -p SourcePath --value)
KV_CONTAINER_FILE=$(systemctl show flightctl-kv.service -p SourcePath --value)

echo "Database container file: $DB_CONTAINER_FILE"
echo "KV store container file: $KV_CONTAINER_FILE"

# Derive drop-in directories from source paths
DB_DROPIN_DIR=$(dirname "$DB_CONTAINER_FILE").d
KV_DROPIN_DIR=$(dirname "$KV_CONTAINER_FILE").d

echo "Database drop-in directory: $DB_DROPIN_DIR"
echo "KV store drop-in directory: $KV_DROPIN_DIR"

# Create drop-in directories
sudo mkdir -p "$DB_DROPIN_DIR"
sudo mkdir -p "$KV_DROPIN_DIR"

# Create drop-in files for port publishing
sudo tee "$DB_DROPIN_DIR/10-publish-port.conf" > /dev/null <<EOF
[Container]
PublishPort=5432:5432
EOF

sudo tee "$KV_DROPIN_DIR/10-publish-port.conf" > /dev/null <<EOF
[Container]
PublishPort=6379:6379
EOF

# Reload systemd and restart services
sudo systemctl daemon-reload
sudo systemctl restart flightctl-db.service flightctl-kv.service

# Verify ports are accessible
netstat -tlnp | grep :5432
netstat -tlnp | grep :6379

# Test database connectivity
pg_isready -h localhost -p 5432

# Test KV store connectivity
redis-cli -h localhost -p 6379 ping
```

### Step 7: Run the Restore Command

Execute the flightctl-restore command using environment variables for database and KV store passwords:

```bash
# Run the restore command with environment variables
DB_PASSWORD="$DB_APP_PASSWORD" KV_PASSWORD="$KV_PASSWORD" ./bin/flightctl-restore
```

Monitor the restore process output for any errors or completion messages.

### Step 8: Clean Up Port Forwarding

After the restore command completes successfully, remove the temporary drop-in files:

```bash
# Get the container file locations dynamically
DB_CONTAINER_FILE=$(systemctl show flightctl-db.service -p SourcePath --value)
KV_CONTAINER_FILE=$(systemctl show flightctl-kv.service -p SourcePath --value)

echo "Database container file: $DB_CONTAINER_FILE"
echo "KV store container file: $KV_CONTAINER_FILE"

# Derive drop-in directories from source paths
DB_DROPIN_DIR=$(dirname "$DB_CONTAINER_FILE").d
KV_DROPIN_DIR=$(dirname "$KV_CONTAINER_FILE").d

echo "Database drop-in directory: $DB_DROPIN_DIR"
echo "KV store drop-in directory: $KV_DROPIN_DIR"

# Remove the drop-in files
sudo rm -f "$DB_DROPIN_DIR/10-publish-port.conf"
sudo rm -f "$KV_DROPIN_DIR/10-publish-port.conf"

# Remove empty drop-in directories
sudo rmdir "$DB_DROPIN_DIR" 2>/dev/null || true
sudo rmdir "$KV_DROPIN_DIR" 2>/dev/null || true

# Reload systemd and restart services
sudo systemctl daemon-reload
sudo systemctl restart flightctl-db.service flightctl-kv.service

# Verify ports are no longer accessible from localhost
netstat -tlnp | grep -E ':(5432|6379)'
```

### Step 9: Restart Flight Control Services

Start the application services back up using systemctl:

```bash
# Start only the application services (database and KV store should already be running)
sudo systemctl start flightctl-api.service
sudo systemctl start flightctl-worker.service
sudo systemctl start flightctl-periodic.service
sudo systemctl start flightctl-alert-exporter.service
sudo systemctl start flightctl-alertmanager-proxy.service

# Verify application services are running
sudo systemctl status flightctl-api.service
sudo systemctl status flightctl-worker.service
sudo systemctl status flightctl-periodic.service
sudo systemctl status flightctl-alert-exporter.service
sudo systemctl status flightctl-alertmanager-proxy.service

# Check container status
sudo podman ps --filter "name=flightctl-"
```

Wait for all services to be fully ready:

```bash
# Monitor service startup
watch "sudo systemctl is-active flightctl-api.service flightctl-worker.service flightctl-periodic.service flightctl-alert-exporter.service flightctl-alertmanager-proxy.service"
```

## Post-Restore Device Status Changes

After completing the restore operation, devices will undergo automatic status transitions based on their state relative to the restored data. Understanding these status changes is crucial for proper post-restore management.

### Device Status Transitions

#### 1. AwaitingReconnect Status

All devices will initially be moved to `AwaitingReconnect` status after the restore operation completes. This indicates that:

- The Flight Control service is waiting for devices to reconnect and report their current state
- Spec rendering is temporarily stopped for these devices
- No configuration changes will be applied until the device reconnects

**What to expect:**

- Devices will remain in this status until they successfully reconnect to the Flight Control service
- Once reconnected, the system will evaluate the device's current state against the restored specifications

#### 2. ConflictPaused Status

If a device's specification in the restored backup is determined to be older than the device's current reported state, the device will be moved to `ConflictPaused` status. This indicates:

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

1. **Review and update the device specification**: if the device is owned by a fleet , review the relevant fleet spec (including template and selector). If the device isn't owned by a fleet, review the device's spec. In either case, review the device's labels and owner.
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
  
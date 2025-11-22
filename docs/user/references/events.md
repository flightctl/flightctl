# Flight Control Events

Flight Control generates events to track activities and state changes across edge devices and resources. These events provide visibility into system operations, device status, and resource lifecycles.

## Overview

Events are automatically created by Flight Control components and serve as a record of system activities. They can be used for:

- **Monitoring and alerting** – Track device health, resource usage, and overall system status  
- **Troubleshooting** – Identify what happened and when  
- **Alert generation** – Events form the basis of system alerts (see [Alerts documentation](alerts.md))  

## Event Structure

Each event includes the following core properties:

| Property              | Description                                                                                 |
|-----------------------|---------------------------------------------------------------------------------------------|
| `apiVersion`          | API version (e.g., `v1alpha1`)                                                              |
| `kind`                | Resource type (always `Event`)                                                              |
| `metadata`            | Standard metadata (name, creation timestamp, annotations)                                   |
| `reason`              | Short, machine-readable explanation of the event                                           |
| `type`                | Event severity (`Normal` or `Warning`)                                                      |
| `message`             | Human-readable description                                                                 |
| `actor`               | User or service that triggered the event (`user:` or `service:` prefix)                     |
| `source`              | Component that generated the event                                                          |
| `involvedObject`      | Resource that the event is related to                                                       |
| `details` (optional)  | Structured event-specific data                                                              |

### Example Event

```yaml
apiVersion: v1alpha1
kind: Event
metadata:
  name: "660e8400-e29b-41d4-a716-446655440001"
  creationTimestamp: "2024-01-15T11:30:00Z"
  annotations:
    flightctl.io/request-id: "req-12346"
reason: "ResourceUpdated"
type: "Normal"
message: "Device 'edge-device-01' was updated"
actor: "user:admin"
source:
  component: "api-server"
involvedObject:
  kind: "Device"
  name: "edge-device-01"
details:
  detailType: "ResourceUpdated"
  updatedFields: ["labels", "owner"]
  newOwner: "production-fleet"
  previousOwner: "staging-fleet"
```

## Event Types

### Device Events

| Category              | Event Reasons                                                                                     |
|-----------------------|--------------------------------------------------------------------------------------------------|
| **Connection Status** | `DeviceConnected`, `DeviceDisconnected`                                                          |
| **Resource Monitoring** | `DeviceCPUCritical`, `DeviceCPUWarning`, `DeviceCPUNormal`, `DeviceMemoryCritical`, `DeviceMemoryWarning`, `DeviceMemoryNormal`, `DeviceDiskCritical`, `DeviceDiskWarning`, `DeviceDiskNormal` |
| **Application Status** | `DeviceApplicationError`, `DeviceApplicationDegraded`, `DeviceApplicationHealthy`              |
| **Device Lifecycle**  | `DeviceIsRebooting`, `DeviceDecommissioned`, `DeviceDecommissionFailed`, `DeviceMultipleOwnersDetected`, `DeviceMultipleOwnersResolved`, `DeviceSpecInvalid`, `DeviceSpecValid` |
| **Content Management** | `DeviceContentUpdating`, `DeviceContentUpToDate`, `DeviceContentOutOfDate`                     |

### Resource Lifecycle Events

| Category               | Event Reasons                                                                                  |
|------------------------|------------------------------------------------------------------------------------------------|
| **General**           | `ResourceCreated`, `ResourceCreationFailed`, `ResourceUpdated`, `ResourceUpdateFailed`, `ResourceDeleted`, `ResourceDeletionFailed` |
| **Enrollment**        | `EnrollmentRequestApproved`, `EnrollmentRequestApprovalFailed`                                 |
| **Fleet Rollouts**    | `FleetRolloutCreated`, `FleetRolloutStarted`, `FleetRolloutBatchCompleted`                     |
| **Repositories**      | `RepositoryAccessible`, `RepositoryInaccessible`                                              |
| **ResourceSync**      | `ResourceSyncAccessible`, `ResourceSyncInaccessible`, `ResourceSyncCommitDetected`, `ResourceSyncParsed`, `ResourceSyncParsingFailed`, `ResourceSyncSynced`, `ResourceSyncSyncFailed`, `ResourceSyncCompleted` |

### System Events

- `InternalTaskFailed`
- `SystemRestored`

## Event Severity

- **Normal** – Standard operational events (e.g., resource creation, updates).  
- **Warning** – Events that indicate potential issues or failures.  

## Viewing Events

Events are listed in descending order of creation time by default, showing the newest first.

### Using the CLI

```bash
# List all events
flightctl get events

# Filter examples
flightctl get events --field-selector="type=Warning"
flightctl get events --field-selector="reason=DeviceDisconnected"
flightctl get events --field-selector="involvedObject.kind=Device"
flightctl get events --field-selector="actor=user:admin"

# Combine filters
flightctl get events --field-selector="involvedObject.kind=Device,type=Warning"

# Limit results
flightctl get events --limit=10

# Output formats
flightctl get events -o json
flightctl get events -o yaml
```

### Using the API

```bash
# Get all events
curl -H "Authorization: Bearer $TOKEN" "https://your-flightctl-server/api/v1/events"

# Get filtered events
curl -H "Authorization: Bearer $TOKEN" \
  "https://your-flightctl-server/api/v1/events?fieldSelector=type=Warning&limit=10"
```

## Filtering and Pagination

### Supported Field Selectors

- `reason` – Event reason (e.g., `DeviceDisconnected`)  
- `type` – Event type (`Normal` or `Warning`)  
- `actor` – Event actor (`user:admin`, `service:api-server`)  
- `involvedObject.kind` – Kind of resource (`Device`, `Fleet`)  
- `involvedObject.name` – Resource name  
- `metadata.creationTimestamp` – Creation timestamp

Supported operators: `=`, `!=`, `in` (e.g., `reason in (DeviceDisconnected,DeviceConnected)`)

### Pagination

```bash
flightctl get events --limit=100
flightctl get events --continue="<token>"
```

## Retention

Events are retained for a configurable period (default: 7 days) and are automatically deleted afterward.

Configure retention in the Flight Control service:

```yaml
service:
  eventRetentionPeriod: "168h"  # 7 days (default)
```

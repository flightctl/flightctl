# Device API Statuses

The following sections document the various statuses reported by Flight Control's Device API. These statuses represent the _service's view_ of the managed device, which is the _last reported_ status from the device agent augmented by service's local context and user policies. The _last reported_ status in turn may be outdated relative to the agent's _current status_ due to reporting delays.

## Device Status

The Device Status represents the availability and health of the device's hardware resources and operating system.

The `device.status.summary` field can have the following values:

| Status              | Description                                                                                                                            | Formal Definition<sup>1</sup> |
|---------------------|----------------------------------------------------------------------------------------------------------------------------------------| ----------------------------- |
| `Online`            | All hardware resources and operating system services are reported to be healthy.                                                       | `!deviceIsDisconnected && !deviceIsRebooting && ∀ r∈{CPU, Memory, Disk}, status.resources[r]∈{Healthy}` |
| `Degraded`          | One or more hardware resources or operating system services are reported to be degraded but in a still functional or recovering state. | `!deviceIsDisconnected && !deviceIsRebooting && ∀ r∈{CPU, Memory, Disk}, status.resources[r]∉{Error, Critical} && ∃ r∈{CPU, Memory, Disk}, status.resources[r]∈{Degraded}` |
| `Error`             | One or more hardware resources or operating system services are reported to be in error or critical state.                             | `!deviceIsDisconnected && !deviceIsRebooting && ∃ r∈{CPU, Memory, Disk}, status.resources[r]∈{Error, Critical}` |
| `Rebooting`         | The device is rebooting.                                                                                                               | `!deviceIsDisconnected && deviceIsRebooting` |
| `Offline`           | The device is disconnected from the service but may still be running.                                                                  | `deviceIsDisconnected` |
| `AwaitingReconnect` | The device is awaiting reconnection after the system was restored.                                                                     | `deviceIsDisconnected` |
| `ConflictPaused`    | The device is paused because the device reported a renderedVersion not known to the service.                                           | `deviceIsDisconnected` |

<sup>1</sup> For the detailed definitions derived from the device specs and statuses, see [Helper Definitions](#helper-definitions).

The following state diagram shows the possible transitions between device statuses, including when the corresponding device lifecycle hooks would be called.

```mermaid
stateDiagram
    classDef transientNotReported stroke-dasharray:5 5
    class ActivatingConfig transientNotReported

    classDef notYetSupported fill:white
    class PoweringOff notYetSupported
    class PoweredOff notYetSupported
    class PoweringOn notYetSupported

    direction LR

    state Unknown {
        Offline
    }
    state Known {
        state PoweredOn {
            direction LR

            [*] --> Online
            Online --> Degraded
            Online --> Error

            Degraded --> Online
            Degraded --> Error

            Error --> Online
            Error --> Degraded

            state beforeRebootingHook <<choice>>
            state afterRebootingHook <<choice>>
            Online --> beforeRebootingHook
            Degraded --> beforeRebootingHook
            Error --> beforeRebootingHook
            beforeRebootingHook --> Rebooting: beforeRebooting hook
            Rebooting --> afterRebootingHook
            afterRebootingHook --> Online: afterRebooting hook
        }

        [*] --> PoweredOn
        PoweredOn --> PoweringOff
        PoweringOff --> PoweredOff
        PoweredOff --> PoweringOn
        PoweringOn --> PoweredOn
    }

    [*] --> Unknown
    Unknown --> Known
    Known --> Unknown
```

## Device Update Status

The Device Update Status represents whether the device's currently running specification (OS, configuration, applications, etc.) matches the user's intent as expressed via the device spec or the fleet's device template.

The `device.status.updated.status` field can have the following values:

| Status | Description | Formal Definition<sup>1</sup> |
| ------ | ----------- | ----------------------------- |
| `UpToDate` | The device is updated to its device spec. If the device is member of a fleet, its device spec is at the same template version as its fleet's device template. | `!deviceIsUpdating && deviceIsUpdatedToDeviceSpec && (deviceIsNotManaged \|\| deviceIsUpdatedToFleetSpec)` |
| `Updating` | The device is in the process of updating to its device spec. | `deviceIsUpdating` |
| `OutOfDate` | The device is not updating and either not updated to its device spec or - if it is member of a fleet - its spec is not yet of the same template version as its fleet's device template. | `!deviceIsUpdating && (!deviceIsUpdatedToDeviceSpec \|\| (deviceIsManaged && !deviceIsUpdatedToFleetSpec))` |
| `Unknown` | The device's agent either never reported status or its last reported status was `Updating` and the device has been disconnected since. | `deviceIsDisconnected && lastStatus == Updating` |

<sup>1</sup> For the detailed definitions derived from the device specs and statuses, see [Helper Definitions](#helper-definitions).

The `device.status.conditions.Updating.Reason` field contains the current state of the update in progress and can take the following values:

| Update State | Description |
| ------------ | ----------- |
| `Preparing` | The agent is validating the desired device spec and downloading dependencies. No changes have been made to the device's configuration yet. |
| `ReadyToUpdate` | The agent has validated the desired spec, downloaded all dependencies, and is ready to update. No changes have been made to the device's configuration yet. |
| `ApplyingUpdate` | The agent has started the update transaction and is writing the update to disk. |
| `Rebooting` | The agent initiated a reboot required to activate the new OS image and configuration. |
| `ActivatingConfig` | (transient, not reported) The agent is activating the new configuration without requiring a reboot. |
| `RollingBack` | The agent has detected an error and is rolling back to the pre-update OS image and configuration. |
| `Updated` | The agent has successfully completed the update and the device is conforming to its device spec. Note that the device's update status may still be reported as `OutOfDate` if the device spec is not yet at the same version as the fleet's device template. |
| `Error` | The agent failed to apply the desired spec and will not retry. The device's OS image and configuration have been rolled back to the pre-update version and have been activated. |

The `device.status.updated.info` field contains a human readable more detailed information about the last state transition.

Before a standalone device update, the service can prepare OS and application deltas. While the update is waiting for preparation, `device.status.conditions[]` includes `DeviceDeltaPreparing` with status `True`.

The `device.status.deltaGeneration.completed` and `device.status.deltaGeneration.total` fields report completed image pairs and total unique image pairs. Completed pairs include successful, failed, and rejected generation jobs. When `completed` equals `total`, every pair has reached a terminal state. Check [per-pair events](../using/troubleshooting.md#inspecting-delta-generation-progress) for success or failure. `device.status.deltaGeneration.lastUpdated` records the last counter update. The condition message includes the same counts.

Fleet-managed updates report preparation through the fleet's `FleetDeltaPreparing` condition. For fleet preparation, see [Configuring delta generation](../using/managing-fleets.md#configuring-delta-generation).

The following state diagram shows the possible transitions between update statuses and states, including when the corresponding device lifecycle hooks would be called.

```mermaid
stateDiagram
    classDef transientNotReported stroke-dasharray:5 5
    class ActivatingConfig transientNotReported

    classDef notYetSupported fill:white
    class Canceled notYetSupported

    direction LR

    Unknown
    state Known {
        direction LR

        UpToDate
        state Updating {
            state beforeUpdatingHook <<choice>>
            state beforeRebootingHook <<choice>>
            state afterRebootingHook <<choice>>
            state afterUpdatingHook <<choice>>

            [*] --> Preparing
            Preparing --> ReadyToUpdate
            Preparing --> Canceled
            Preparing --> Error: on error
            ReadyToUpdate --> beforeUpdatingHook: beforeUpdatingHook
            ReadyToUpdate --> Canceled
            beforeUpdatingHook --> ApplyingUpdate
            ApplyingUpdate --> beforeRebootingHook: if OS updated
            ApplyingUpdate --> ActivatingConfig: if OS not updated
            ApplyingUpdate --> RollingBack: on error
            beforeRebootingHook --> Rebooting: beforeRebooting hook
            Rebooting --> afterRebootingHook
            afterRebootingHook --> ActivatingConfig: afterRebooting hook
            Rebooting --> RollingBack: on greenboot failed
            ActivatingConfig --> afterUpdatingHook: afterUpdating hook
            afterUpdatingHook --> Updated
            afterUpdatingHook --> RollingBack: on error
            RollingBack --> Error

            Updated --> [*]
            Canceled --> [*]
            Error --> [*]
        }

        [*] --> UpToDate
        UpToDate --> OutOfDate
        OutOfDate --> Updating
        Updating --> UpToDate
        Updating --> OutOfDate
    }

    [*] --> Unknown
    Unknown --> Known
    Known --> Unknown
```

### OS delta status

You can inspect delta eligibility and the result of an OS delta update in the device's YAML or JSON output:

```console
flightctl get device <device_name> -o yaml
```

The following device status fields describe delta capability and the result for the desired OS image:

| Field | Description |
| ----- | ----------- |
| `device.status.systemInfo.deltaEligible` | `true` when the agent has the `oci-delta` tool available. This field may be absent when an older agent does not report it. |
| `device.status.os.lastDelta.outcome` | The agent-reported OS result. `NotUsed` means delta application was skipped without a delta failure, `Applied` means the delta was applied, and `Fallback` means a delta attempt failed and the agent attempted a full image pull. The field is omitted until an outcome is reported. |
| `device.status.os.lastDelta.fallbackReason` | The reason for a recorded delta pull or apply failure. It is omitted when the recorded outcome has no fallback reason. |
| `device.status.os.deltaSize` | Payload size in IEC units, such as MiB or GiB, for a control-plane-generated OS delta. Present when a delta was generated and its payload size is known. |

OS delta application also requires bootc.

OS outcomes persist across agent restarts for the same desired OS image. If no matching delta is found, the agent records `NotUsed` only when that image has no recorded outcome. Otherwise, it retains the existing outcome, including a previous `Fallback` and its reason. An already running or cached OS image is also recorded as `NotUsed` only when that image has no recorded outcome.

The size field measures only the control-plane-generated delta artifact for the desired OS image. It is a payload measurement taken during generation.

Use `-o yaml` or `-o json` for the detailed delta fields. Device summary capability counts report only `osMode`.

## Application Status

The Application Status represents a summary of the availability and health of all applications on the system.

The `device.status.applicationSummary` field can have the following values:

| Status | Description | Formal Definition<sup>1</sup> |
| ------ | ----------- | ----------------------------- |
| `NoApplications` | No applications are defined for the device. | `!deviceIsDisconnected && len(status.applications) == 0` |
| `Healthy` | All applications are reported to be in service or have successfully completed. | `!deviceIsDisconnected && len(status.applications) > 0 && ∀ a∈status.applications, status.applications[a]∈{Running, Completed}` |
| `Degraded` | One or more applications are reported to not be in service but still in a starting or recovering state. | `!deviceIsDisconnected && ∀ a∈status.applications, status.applications[a]∉{Error} && ∃ a∈status.applications, status.applications[a]∈{Preparing, Starting}` |
| `Error` | One or more applications are reported to be in error state. | `!deviceIsDisconnected && ∃ a∈status.applications, status.applications[a]∈{Error}` |
| `Unknown` | The device's agent either never reported status or the device is currently disconnected. | `deviceIsDisconnected` |

<sup>1</sup> For the detailed definitions derived from the device specs and statuses, see [Helper Definitions](#helper-definitions).

The following state diagram shows the possible transitions between application summary statuses.

```mermaid
stateDiagram
    classDef transientNotReported stroke-dasharray:5 5
    class ActivatingConfig transientNotReported

    classDef notYetSupported fill:white
    class PoweringOff notYetSupported
    class PoweredOff notYetSupported
    class PoweringOn notYetSupported

    direction LR

    Unknown
    state Known {
        direction LR

        [*] --> NoApplications
        NoApplications --> Healthy
        NoApplications --> Degraded
        NoApplications --> Error

        Healthy --> NoApplications
        Healthy --> Degraded
        Healthy --> Error

        Degraded --> NoApplications
        Degraded --> Healthy
        Degraded --> Error

        Error --> NoApplications
        Error --> Healthy
        Error --> Degraded
    }

    [*] --> Unknown
    Unknown --> Known
    Known --> Unknown
```

### Application delta status

Each application's aggregated delta result appears in `device.status.applications[].lastDelta`. The agent tracks image targets separately and combines their outcomes into one status for the application.

| Field | Description |
| ----- | ----------- |
| `device.status.applications[].lastDelta.outcome` | The agent-reported application result, as described in the following table. Present after the agent reports an outcome. |
| `device.status.applications[].lastDelta.fallbackReason` | One representative reason for a full-pull fallback after a delta failure or an unusable hinted delta. Present when at least one target falls back with a recorded reason; the application outcome can be `Fallback` or `Partial`. |
| `device.status.applications[].imageDigests[].image` | Image reference used by the application on the device. |
| `device.status.applications[].imageDigests[].digest` | Registry digest associated with the image. For a multi-platform image, this may be the platform-specific digest selected by the runtime. If only an opaque ID is available, an immutable reference's digest may be reported. Present when a digest is known. |
| `device.status.applications[].deltaSize` | Sum of only the control-plane-generated delta payload sizes for this application update, in IEC units. Present when at least one delta was generated and every generated payload size is known. |

`imageDigests[]` can include the application's main image, nested images, and image-backed volumes. Flight Control uses the reference and digest pairs to identify source images for delta generation.

Inspect each application's generated delta payload size at `status.applications[].deltaSize` and the generated OS delta payload size at `status.os.deltaSize`.

Inspect application delta fields in the device's YAML or JSON output:

```console
flightctl get device <device_name> -o yaml
```

The following outcomes describe the application as a whole. The agent reuses image targets already present with the desired digest when preparing the remaining targets. If any target applies a delta while another target uses regular preparation or falls back to a full pull, the application outcome is `Partial`. `Fallback` applies when no target applies a delta and at least one target records a failed delta attempt.

| Outcome | Description |
| ------- | ----------- |
| `NotRequired` | All image targets are already present on the device with the correct digest. The agent reuses those images. |
| `NotUsed` | Every target requiring preparation uses the regular image path because delta application was skipped. The agent pulls full images as needed. |
| `Applied` | At least one delta was applied successfully. Other image targets also applied deltas or already matched their desired digests. |
| `Fallback` | No target applied a delta, and at least one delta attempt failed. For an OCI artifact package with a supplied hint, the agent records this outcome and uses the regular artifact pull path. It uses full image pulls for other targets requiring preparation and reuses already matching images. |
| `Partial` | The application combines at least one successful delta application with regular image preparation or a full-pull fallback for another image target. |

Results persist across agent restarts while the application specification and image targets remain unchanged. A cache check can change an earlier `NotUsed` result to `NotRequired` after verifying the desired digest. Cache checks retain earlier `Applied` or `Fallback` results for the same target.

## Lifecycle status

The Lifecycle Status represents whether the device is available to be managed and assigned to do work or is moving to an end-of-life state.

The `device.status.lifecycle` field can have the following values:

| Status | Description | Formal Definition<sup>1</sup> |
| ------ | ----------- | ----------------------------- |
| `Enrolled` | The device's Enrollment Request was approved and it will be able to connect to management to carry out normal operations using its management certificate. | `enrollmentCompleted(device) && device.spec.decommissioning == nil` |
| `Decommissioning` | The device has been requested to decommission by a user and is no longer available to carry out normal operations. | `enrollmentCompleted(device) && device.spec.decommissioning != nil && (device.conditions["DeviceDecommissioning"] == nil \|\| !(device.conditions["DeviceDecommissioning"].reason ∈ {Completed, Error}))` |
| `Decommissioned` | The device has either completed its decommissioning process or has encountered an unrecoverable error in doing so. | `enrollmentCompleted(device) && device.spec.decommissioning != nil && device.conditions["DeviceDecommissioning"].reason ∈ {Completed, Error}` |
| `Unknown` | No device available through management should be in this state. | `!enrollmentCompleted(device)` |

<sup>1</sup> For the detailed definitions derived from the device specs and statuses, see [Helper Definitions](#helper-definitions).
For the allowed transitions between these Statuses, see the [state diagram](#state-diagram-for-device-lifecycle) below.

The `device.status.conditions` field may contain a Condition of type `DeviceDecommissioning` whose `Condition.Reason` field contains the current state of a device's progress on a request to decommission that it has received. It can contain the following values:

| Decommissioning State | Description |
| ------------ | ----------- |
| `Started` | The agent has received the request to decommission from the service and will take a series of previously defined decommissioning actions. |
| `Completed` | The agent has completed its decommissioning process, up until the point of wiping its management certificate that is used to communicate with the service. |
| `Error` | The agent has encountered an unrecoverable error during its decommissioning process and will not be able to take further actions. |

NOTE: the Decommissioning State reflects the device perspective. It is possible for the user to request decommissioning (the device will be marked as "decommissioning" server-side) but for the device not to have received this request. This corresponds to the Requested state in the diagram below.

The following state diagram shows the possible transitions between lifecycle statuses and states. Note that none of these transitions are guaranteed to occur, and the device may remain in any one of these states indefinitely, including the Decommissioning state.

### State diagram for device lifecycle

```mermaid
stateDiagram
    direction LR
        state Decommissioning {
            [*] --> Requested
            Requested --> Started
        }

        state Decommissioned {
            [*] --> Completed
            [*] --> Error
        }

        [*] --> Unknown
        Unknown --> Enrolled
        Enrolled --> Decommissioning
        Decommissioning --> Decommissioned
```

## Helper Definitions

The formal definition uses the following helper definitions:

```golang
// A device is assumed disconnected if its agent hasn't sent an update for the duration of a disconnectionTimeout.
deviceIsDisconnected := device.lastSeen + disconnectionTimeout < time.Now()

// A device is not managed by a fleet if its owner field is unset.
deviceIsNotManaged := len(device.metadata.owner) == 0

// A device is rebooting when the agent sets the "Rebooting" condition to true.
deviceIsRebooting := device.status.conditions.rebooting == true

// A device is updating when the agent sets the "Updating" condition to true.
deviceIsUpdating := device.status.conditions.updating == true

// A device is updated to its device spec when the version of the device spec that the agent reports as running
// equals the version rendered to the device by the service.
deviceIsUpdatedToDeviceSpec := device.status.config.renderedVersion == device.metadata.annotations.renderedVersion

// A device is updated to it's fleet's spec when it is updated to its device spec and that device spec's
// template version matches the device's fleet's template version.
deviceIsUpdatedToFleetSoec := deviceIsUpdatedToDeviceSpec && device.metadata.annotations.templateVersion == fleet[device.metadata.owner].spec.templateVersion

// A device has completed enrollment if there exists an EnrollmentRequest record with the device's ID and that record contains a valid `er.status.certificate`.
enrollmentCompleted := enrollmentRequests[device.metadata.name] != nil && enrollmentRequests[device.metadata.name].status.certificate != nil
```

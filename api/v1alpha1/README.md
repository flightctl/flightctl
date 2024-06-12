# FlightCtl API

This document is intended to provide context to the API definition.

## Device.Status

### Device.Status.Updated

| Name    | Desc |
| -------- | ------- |
| Status  | True if the device has been updated to the most recent version, otherwise False |
| Reason   | A machine readable why the status is in its current state |
| Message | A human readable details of why the status is in its current state |

### Device.Status.Applications
An application contains one or more containers.

| Name    | Desc |
| -------- | ------- |
| ID | Unique identifier for the workload |
| Name | A human readable name for the workload |
| Workloads | One or more workload |

### Device.Status.Workload
A workload represents a logical unit of work. Valid workloads types include containers and virtual machines.

| Name    | Desc |
| -------- | ------- |
| ID | Unique identifier for the container |
| Name | A human readable name for the container |
| Status | The current lifecycle phase of an container |
| Ready  | The number of containers which are ready in an container |
| Restarts | The number of times a container inside an application has been restarted |

### Device.Status.Application.Status

| Name    | Desc |
| -------- | ------- |
| Preparing | The application is being validated and dependencies are being created/pulled |
| Starting | The application is being started but is not yet available |
| Running | The application has been successfully scheduled and the process has not existed |
| Error | Part of all of the application has been terminated with a non zero exit code |
| Unknown | The Device us unable to determine the exact status |
| Completed | The application has exited successfully with a zero exit code. |

### Conditions

#### General
These conditions that cover the general operation of the Agent/Device

| Name    | Desc |
| -------- | ------- |
| Available   | True if the Device is available for reconciling changes to the Device Spec, otherwise False  |
| Updating | True if the Agent is in the process of reconciling Spec, otherwise False    |
| Degraded    | True if the Device has reported a transient retryable error or failure during its operations, otherwise False |

  `*` Written by service

#### Workloads
These are the core conditions that cover the ability of the Device to accept/manage workloads.

| Name    | Desc |
| -------- | ------- |
| WorkloadsAvailable  | True if the Device reports no resource pressure and is available to accept new workloads, otherwise is False. |
| WorkloadsDegraded   | True if one or more of the workload has reported error, otherwise False |
| DiskPressure | True if pressure exists on the disk size—that is, if the disk capacity is low, otherwise False. |
| PIDPressure | True if pressure exists on the processes—that is, if there are too many processes on the node, otherwise False. | 
| MemoryPressure |True if pressure exists on the Device memory—that is, if the Device memory is low, otherwise False. |
| CPUPressure |True if Device is experiencing high CPU utilization that might affect the performance of the workloads running on that Device, otherwise False. |

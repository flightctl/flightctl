# FlightCtl API

This document is intended to provide context to the API definition.

## Device.Status

### Conditions
These are the core conditions that cover the ability of the device to accept/manage workloads.

| Name    | Desc |
| -------- | ------- |
| Ready  | True if the Device is ready and can accept new containers otherwise False. This is also the only condition which can be set by the server, otherwise False|
| DiskPressure | True if pressure exists on the disk size—that is, if the disk capacity is low; otherwise False |
| PIDPressure | True if pressure exists on the processes—that is, if there are too many processes on the node, otherwise False | 
| MemoryPressure |True if pressure exists on the device memory—that is, if the device memory is low, otherwise False
| CPUPressure |True if device is experiencing high CPU utilization that might affect the performance of the workloads running on that device, otherwise False.

### Config.Conditions
These are the core conditions that cover the operation of the agent's device config controller

| Name    | Desc |
| -------- | ------- |
| Available  | True if the controller is available for deploying configurations and postAction commands otherwise False  |
| Progressing | True if the controller is in the process of reconciling Spec, otherwise False    |
| Degraded    |  True if the controllers functionality is impaired in some way but not unavailable, otherwise False |
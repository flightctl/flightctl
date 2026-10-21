# Flight Control API

This document is intended to provide context to the API definition.

## Known condition reasons

The standalone `*ConditionReason` schemas in `openapi.yaml` are discovery
catalogs for generated clients, not constraints on `ConditionBase.reason` or
`Condition.reason`. Both fields remain plain strings. These catalogs are
non-exhaustive: clients must accept and preserve unknown strings, including when
reading conditions from newer servers or agents. Existing exported Go constants
and runtime values are unchanged; generated catalog constants are additive.

| Catalog | Resource and condition | Known values |
| --- | --- | --- |
| EnrollmentApprovalConditionReason | EnrollmentRequest Approved; CertificateSigningRequest Approved/Denied | Approved, ManuallyApproved (EnrollmentRequest only), Denied (CertificateSigningRequest only) |
| CertificateSigningRequestFailureConditionReason | CertificateSigningRequest Failed | SigningFailed |
| TpmConditionReason | EnrollmentRequest and CertificateSigningRequest TPMVerified | TPMVerificationFailed (both); TPMVerificationSucceeded (CertificateSigningRequest); TPMChallengeRequired, TPMChallengeFailed, TPMChallengeSucceeded (EnrollmentRequest) |
| RepositoryConditionReason | Repository Accessible | Accessible, Inaccessible |
| ResourceSyncConditionReason | ResourceSync Accessible | accessible, repository resource not found, failed to clone repository, path not found in repository |
| ResourceSyncConditionReason | ResourceSync ResourceParsed; Synced | success, fail; Synced also uses NewHashDetected |
| ValidationConditionReason | Fleet Valid; Device SpecValid | Valid, Invalid |
| FleetRolloutConditionReason | Fleet RolloutInProgress | Inactive, Active, Suspended, Waiting |
| DeviceUpdateConditionReason | Device Updating (update states); legacy Bootstrapping has no current condition type mapping | Bootstrapping, Preparing, ReadyToUpdate, ApplyingUpdate, Rebooting, Updated, Canceled, Error, RollingBack, Retrying |
| DeviceDecommissionConditionReason | Device DeviceDecommissioning | Started, Completed, Error |
| DeviceOwnershipConditionReason | Device MultipleOwners | MultipleOwners (the false condition may have an empty reason) |
| DeviceLabelsConditionReason | Device LabelsSynced | Reconciled, ReconciliationFailed |
| LabelSyncMappingConditionReason | LabelSyncMapping Ready | Pending, Degraded, Success |
| DeltaPreparingConditionReason | Fleet FleetDeltaPreparing; Device DeviceDeltaPreparing | DeltaGenerationInProgress |
| EnrollmentHooksConditionReason | Device EnrollmentHooks | NotifyPending, Pending, Failed, Succeeded, Continued, ManualOverride |
| EnrollmentHookPolicyConditionReason | EnrollmentHookPolicy Ready | ValidationPassed |

The inventory follows the service handlers (enrollment, TPM verification, CSR
signing, repository checks, label reconciliation and policy validation),
`internal/tasks` (resource sync, fleet validation/selection and device rendering),
`internal/rollout/device_selection/condition_emitter.go`,
`internal/delta_worker/service/status.go`, label mapping store scans, CLI approval
and denial, and agent update, bootstrap, lifecycle and enrollment hook paths.
`TPMChallengeRequired` is emitted with `TPMVerified=False` during enrollment.
ResourceSync's legacy lowercase and space-containing reasons are deliberately
preserved. Bootstrapping, Canceled and Retrying are declared core constants;
they are included for discovery even though current condition producers do not
emit them.

Reasons accepted through condition/status APIs and enrollment hook reports can
be supplied by clients or agents and are passed through, rather than drawn from
a closed set. Error details are generally carried in `message`, not synthesized
into additional catalog values. Event reasons, operation `Status.reason`, audit
reasons and Image Builder reason catalogs are separate APIs and are not included.

## Device.Status

### Conditions

These are the core conditions that cover the ability of the device to accept/manage workloads.

| Name    | Desc |
| -------- | ------- |
| Ready  | True if the Device is ready and can accept new containers otherwise False. This is also the only condition which can be set by the server, otherwise False. |
| DiskPressure | True if pressure exists on the disk size—that is, if the disk capacity is low; otherwise False. |
| PIDPressure | True if pressure exists on the processes—that is, if there are too many processes on the node, otherwise False. | 
| MemoryPressure |True if pressure exists on the device memory—that is, if the device memory is low, otherwise False. |
| CPUPressure |True if device is experiencing high CPU utilization that might affect the performance of the workloads running on that device, otherwise False. |
| SystemdUnitsRunning | True is all specified systemd units are running, otherwise False. |
| ContainersRunning | True is all specified containers are running, otherwise False. |

### Conditions

These conditions that cover the operation of the agent

| Name    | Desc |
| -------- | ------- |
| Available  | True if the controller is available for deploying configurations and postAction commands otherwise False  |
| Progressing | True if the controller is in the process of reconciling Spec, otherwise False    |
| Degraded    | True if the controllers functionality is impaired in some way but not unavailable, otherwise False |

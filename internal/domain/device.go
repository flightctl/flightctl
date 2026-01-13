package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Resource Types ==========

type Device = v1beta1.Device
type DeviceList = v1beta1.DeviceList
type DeviceSpec = v1beta1.DeviceSpec
type DeviceStatus = v1beta1.DeviceStatus

// ========== Status Subtypes ==========

type DeviceSummaryStatus = v1beta1.DeviceSummaryStatus
type DeviceApplicationStatus = v1beta1.DeviceApplicationStatus
type DeviceApplicationsSummaryStatus = v1beta1.DeviceApplicationsSummaryStatus
type DeviceConfigStatus = v1beta1.DeviceConfigStatus
type DeviceIntegrityStatus = v1beta1.DeviceIntegrityStatus
type DeviceIntegrityCheckStatus = v1beta1.DeviceIntegrityCheckStatus
type DeviceLifecycleStatus = v1beta1.DeviceLifecycleStatus
type DeviceUpdatedStatus = v1beta1.DeviceUpdatedStatus
type DeviceResourceStatus = v1beta1.DeviceResourceStatus
type DeviceLastSeen = v1beta1.DeviceLastSeen
type DeviceOsStatus = v1beta1.DeviceOsStatus
type DeviceSystemInfo = v1beta1.DeviceSystemInfo
type CustomDeviceInfo = v1beta1.CustomDeviceInfo

// ========== Spec Subtypes ==========

type DeviceOsSpec = v1beta1.DeviceOsSpec
type DeviceUpdatePolicySpec = v1beta1.DeviceUpdatePolicySpec

// ========== Operations ==========

type DeviceConsole = v1beta1.DeviceConsole
type DeviceDecommission = v1beta1.DeviceDecommission
type DeviceResumeRequest = v1beta1.DeviceResumeRequest
type DeviceResumeResponse = v1beta1.DeviceResumeResponse

// ========== Aggregation ==========

type DevicesSummary = v1beta1.DevicesSummary
type DeviceCompletionCount = v1beta1.DeviceCompletionCount

// ========== Enum Types ==========

type DeviceLifecycleStatusType = v1beta1.DeviceLifecycleStatusType
type DeviceSummaryStatusType = v1beta1.DeviceSummaryStatusType
type DeviceUpdatedStatusType = v1beta1.DeviceUpdatedStatusType
type DeviceResourceStatusType = v1beta1.DeviceResourceStatusType
type DeviceIntegrityStatusSummaryType = v1beta1.DeviceIntegrityStatusSummaryType
type DeviceIntegrityCheckStatusType = v1beta1.DeviceIntegrityCheckStatusType
type DeviceDecommissionTargetType = v1beta1.DeviceDecommissionTargetType
type DeviceLifecycleHookType = v1beta1.DeviceLifecycleHookType

// ========== Device Lifecycle Status Constants ==========

const (
	DeviceLifecycleStatusEnrolled        = v1beta1.DeviceLifecycleStatusEnrolled
	DeviceLifecycleStatusDecommissioned  = v1beta1.DeviceLifecycleStatusDecommissioned
	DeviceLifecycleStatusDecommissioning = v1beta1.DeviceLifecycleStatusDecommissioning
	DeviceLifecycleStatusUnknown         = v1beta1.DeviceLifecycleStatusUnknown
)

// ========== Device Summary Status Constants ==========

const (
	DeviceSummaryStatusAwaitingReconnect = v1beta1.DeviceSummaryStatusAwaitingReconnect
	DeviceSummaryStatusConflictPaused    = v1beta1.DeviceSummaryStatusConflictPaused
	DeviceSummaryStatusDegraded          = v1beta1.DeviceSummaryStatusDegraded
	DeviceSummaryStatusError             = v1beta1.DeviceSummaryStatusError
	DeviceSummaryStatusOnline            = v1beta1.DeviceSummaryStatusOnline
	DeviceSummaryStatusPoweredOff        = v1beta1.DeviceSummaryStatusPoweredOff
	DeviceSummaryStatusRebooting         = v1beta1.DeviceSummaryStatusRebooting
	DeviceSummaryStatusUnknown           = v1beta1.DeviceSummaryStatusUnknown
)

// ========== Device Updated Status Constants ==========

const (
	DeviceUpdatedStatusOutOfDate = v1beta1.DeviceUpdatedStatusOutOfDate
	DeviceUpdatedStatusUnknown   = v1beta1.DeviceUpdatedStatusUnknown
	DeviceUpdatedStatusUpToDate  = v1beta1.DeviceUpdatedStatusUpToDate
	DeviceUpdatedStatusUpdating  = v1beta1.DeviceUpdatedStatusUpdating
)

// ========== Device Resource Status Constants ==========

const (
	DeviceResourceStatusCritical = v1beta1.DeviceResourceStatusCritical
	DeviceResourceStatusError    = v1beta1.DeviceResourceStatusError
	DeviceResourceStatusHealthy  = v1beta1.DeviceResourceStatusHealthy
	DeviceResourceStatusUnknown  = v1beta1.DeviceResourceStatusUnknown
	DeviceResourceStatusWarning  = v1beta1.DeviceResourceStatusWarning
)

// ========== Device Integrity Status Constants ==========

const (
	DeviceIntegrityStatusFailed      = v1beta1.DeviceIntegrityStatusFailed
	DeviceIntegrityStatusUnknown     = v1beta1.DeviceIntegrityStatusUnknown
	DeviceIntegrityStatusUnsupported = v1beta1.DeviceIntegrityStatusUnsupported
	DeviceIntegrityStatusVerified    = v1beta1.DeviceIntegrityStatusVerified
)

const (
	DeviceIntegrityCheckStatusFailed      = v1beta1.DeviceIntegrityCheckStatusFailed
	DeviceIntegrityCheckStatusUnknown     = v1beta1.DeviceIntegrityCheckStatusUnknown
	DeviceIntegrityCheckStatusUnsupported = v1beta1.DeviceIntegrityCheckStatusUnsupported
	DeviceIntegrityCheckStatusVerified    = v1beta1.DeviceIntegrityCheckStatusVerified
)

// ========== Device Decommission Target Constants ==========

const (
	DeviceDecommissionTargetTypeFactoryReset = v1beta1.DeviceDecommissionTargetTypeFactoryReset
	DeviceDecommissionTargetTypeUnenroll     = v1beta1.DeviceDecommissionTargetTypeUnenroll
)

// ========== Device Lifecycle Hook Constants ==========

const (
	DeviceLifecycleHookAfterRebooting  = v1beta1.DeviceLifecycleHookAfterRebooting
	DeviceLifecycleHookAfterUpdating   = v1beta1.DeviceLifecycleHookAfterUpdating
	DeviceLifecycleHookBeforeRebooting = v1beta1.DeviceLifecycleHookBeforeRebooting
	DeviceLifecycleHookBeforeUpdating  = v1beta1.DeviceLifecycleHookBeforeUpdating
)

// ========== Event Details Types ==========

type DeviceMultipleOwnersDetectedDetails = v1beta1.DeviceMultipleOwnersDetectedDetails
type DeviceMultipleOwnersDetectedDetailsDetailType = v1beta1.DeviceMultipleOwnersDetectedDetailsDetailType
type DeviceMultipleOwnersResolvedDetails = v1beta1.DeviceMultipleOwnersResolvedDetails
type DeviceMultipleOwnersResolvedDetailsDetailType = v1beta1.DeviceMultipleOwnersResolvedDetailsDetailType
type DeviceMultipleOwnersResolvedDetailsResolutionType = v1beta1.DeviceMultipleOwnersResolvedDetailsResolutionType
type DeviceOwnershipChangedDetails = v1beta1.DeviceOwnershipChangedDetails
type DeviceOwnershipChangedDetailsDetailType = v1beta1.DeviceOwnershipChangedDetailsDetailType

const (
	DeviceMultipleOwnersDetected = v1beta1.DeviceMultipleOwnersDetected
	DeviceMultipleOwnersResolved = v1beta1.DeviceMultipleOwnersResolved
	DeviceOwnershipChanged       = v1beta1.DeviceOwnershipChanged
	FleetDeleted                 = v1beta1.FleetDeleted
	NoMatch                      = v1beta1.NoMatch
	SingleMatch                  = v1beta1.SingleMatch
)

// ========== Console Types ==========

type TerminalSize = v1beta1.TerminalSize
type DeviceConsoleSessionMetadata = v1beta1.DeviceConsoleSessionMetadata
type DeviceCommand = v1beta1.DeviceCommand

// ========== Utility Functions ==========

var NewDeviceStatus = v1beta1.NewDeviceStatus
var DeviceSpecsAreEqual = v1beta1.DeviceSpecsAreEqual
var GetNextDeviceRenderedVersion = v1beta1.GetNextDeviceRenderedVersion

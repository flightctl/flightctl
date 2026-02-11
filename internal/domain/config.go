package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Config Provider Types ==========

type ConfigProviderSpec = v1beta1.ConfigProviderSpec
type GitConfigProviderSpec = v1beta1.GitConfigProviderSpec
type HttpConfigProviderSpec = v1beta1.HttpConfigProviderSpec
type InlineConfigProviderSpec = v1beta1.InlineConfigProviderSpec
type KubernetesSecretProviderSpec = v1beta1.KubernetesSecretProviderSpec

// ConfigProviderType discriminator type
type ConfigProviderType = v1beta1.ConfigProviderType

const (
	GitConfigProviderType        = v1beta1.GitConfigProviderType
	HttpConfigProviderType       = v1beta1.HttpConfigProviderType
	InlineConfigProviderType     = v1beta1.InlineConfigProviderType
	KubernetesSecretProviderType = v1beta1.KubernetesSecretProviderType
)

// ========== File Types ==========

type FileSpec = v1beta1.FileSpec
type FileContent = v1beta1.FileContent
type FileMetadata = v1beta1.FileMetadata
type FileOperation = v1beta1.FileOperation
type AbsolutePath = v1beta1.AbsolutePath
type RelativePath = v1beta1.RelativePath

const (
	FileOperationCreated = v1beta1.FileOperationCreated
	FileOperationRemoved = v1beta1.FileOperationRemoved
	FileOperationUpdated = v1beta1.FileOperationUpdated
)

// ========== Encoding ==========

type EncodingType = v1beta1.EncodingType

const (
	EncodingBase64 = v1beta1.EncodingBase64
	EncodingPlain  = v1beta1.EncodingPlain
)

// ========== Hooks ==========

type HookAction = v1beta1.HookAction
type HookActionRun = v1beta1.HookActionRun
type HookCondition = v1beta1.HookCondition
type HookConditionExpression = v1beta1.HookConditionExpression
type HookConditionPathOp = v1beta1.HookConditionPathOp

// HookActionType discriminator
type HookActionType = v1beta1.HookActionType

const (
	HookActionTypeRun = v1beta1.HookActionTypeRun
)

// HookConditionType discriminator
type HookConditionType = v1beta1.HookConditionType

const (
	HookConditionTypeExpression = v1beta1.HookConditionTypeExpression
	HookConditionTypePathOp     = v1beta1.HookConditionTypePathOp
)

// ========== Resource Monitors ==========

type ResourceMonitor = v1beta1.ResourceMonitor
type ResourceMonitorSpec = v1beta1.ResourceMonitorSpec
type CpuResourceMonitorSpec = v1beta1.CpuResourceMonitorSpec
type MemoryResourceMonitorSpec = v1beta1.MemoryResourceMonitorSpec
type DiskResourceMonitorSpec = v1beta1.DiskResourceMonitorSpec
type ResourceAlertRule = v1beta1.ResourceAlertRule
type ResourceAlertSeverityType = v1beta1.ResourceAlertSeverityType

const (
	ResourceAlertSeverityCritical = v1beta1.ResourceAlertSeverityTypeCritical
	ResourceAlertSeverityInfo     = v1beta1.ResourceAlertSeverityTypeInfo
	ResourceAlertSeverityWarning  = v1beta1.ResourceAlertSeverityTypeWarning

	// Direct aliases for compatibility
	ResourceAlertSeverityTypeCritical = v1beta1.ResourceAlertSeverityTypeCritical
	ResourceAlertSeverityTypeInfo     = v1beta1.ResourceAlertSeverityTypeInfo
	ResourceAlertSeverityTypeWarning  = v1beta1.ResourceAlertSeverityTypeWarning
)

// ========== Systemd Types ==========

type SystemdUnitStatus = v1beta1.SystemdUnitStatus
type SystemdActiveStateType = v1beta1.SystemdActiveStateType
type SystemdEnableStateType = v1beta1.SystemdEnableStateType
type SystemdLoadStateType = v1beta1.SystemdLoadStateType

const (
	SystemdActiveStateActivating   = v1beta1.SystemdActiveStateActivating
	SystemdActiveStateActive       = v1beta1.SystemdActiveStateActive
	SystemdActiveStateDeactivating = v1beta1.SystemdActiveStateDeactivating
	SystemdActiveStateFailed       = v1beta1.SystemdActiveStateFailed
	SystemdActiveStateInactive     = v1beta1.SystemdActiveStateInactive
	SystemdActiveStateMaintenance  = v1beta1.SystemdActiveStateMaintenance
	SystemdActiveStateRefreshing   = v1beta1.SystemdActiveStateRefreshing
	SystemdActiveStateReloading    = v1beta1.SystemdActiveStateReloading
	SystemdActiveStateUnknown      = v1beta1.SystemdActiveStateUnknown
)

const (
	SystemdEnableStateAlias          = v1beta1.SystemdEnableStateAlias
	SystemdEnableStateBad            = v1beta1.SystemdEnableStateBad
	SystemdEnableStateDisabled       = v1beta1.SystemdEnableStateDisabled
	SystemdEnableStateEmpty          = v1beta1.SystemdEnableStateEmpty
	SystemdEnableStateEnabled        = v1beta1.SystemdEnableStateEnabled
	SystemdEnableStateEnabledRuntime = v1beta1.SystemdEnableStateEnabledRuntime
	SystemdEnableStateGenerated      = v1beta1.SystemdEnableStateGenerated
	SystemdEnableStateIndirect       = v1beta1.SystemdEnableStateIndirect
	SystemdEnableStateLinked         = v1beta1.SystemdEnableStateLinked
	SystemdEnableStateLinkedRuntime  = v1beta1.SystemdEnableStateLinkedRuntime
	SystemdEnableStateMasked         = v1beta1.SystemdEnableStateMasked
	SystemdEnableStateMaskedRuntime  = v1beta1.SystemdEnableStateMaskedRuntime
	SystemdEnableStateStatic         = v1beta1.SystemdEnableStateStatic
	SystemdEnableStateTransient      = v1beta1.SystemdEnableStateTransient
	SystemdEnableStateUnknown        = v1beta1.SystemdEnableStateUnknown
)

const (
	SystemdLoadStateBadSetting = v1beta1.SystemdLoadStateBadSetting
	SystemdLoadStateError      = v1beta1.SystemdLoadStateError
	SystemdLoadStateLoaded     = v1beta1.SystemdLoadStateLoaded
	SystemdLoadStateMasked     = v1beta1.SystemdLoadStateMasked
	SystemdLoadStateMerged     = v1beta1.SystemdLoadStateMerged
	SystemdLoadStateNotFound   = v1beta1.SystemdLoadStateNotFound
	SystemdLoadStateStub       = v1beta1.SystemdLoadStateStub
	SystemdLoadStateUnknown    = v1beta1.SystemdLoadStateUnknown
)

// ========== Update Schedule ==========

type UpdateSchedule = v1beta1.UpdateSchedule

// ========== Version ==========

type Version = v1beta1.Version

// ========== Secure String ==========

type SecureString = v1beta1.SecureString

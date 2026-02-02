package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Metadata ==========

type ObjectMeta = v1beta1.ObjectMeta
type ListMeta = v1beta1.ListMeta
type ObjectReference = v1beta1.ObjectReference

// ========== Conditions ==========

type Condition = v1beta1.Condition
type ConditionBase = v1beta1.ConditionBase
type ConditionType = v1beta1.ConditionType
type ConditionStatus = v1beta1.ConditionStatus

const (
	ConditionStatusTrue    = v1beta1.ConditionStatusTrue
	ConditionStatusFalse   = v1beta1.ConditionStatusFalse
	ConditionStatusUnknown = v1beta1.ConditionStatusUnknown
)

// Condition type constants
const (
	ConditionTypeCertificateSigningRequestApproved    = v1beta1.ConditionTypeCertificateSigningRequestApproved
	ConditionTypeCertificateSigningRequestDenied      = v1beta1.ConditionTypeCertificateSigningRequestDenied
	ConditionTypeCertificateSigningRequestFailed      = v1beta1.ConditionTypeCertificateSigningRequestFailed
	ConditionTypeCertificateSigningRequestTPMVerified = v1beta1.ConditionTypeCertificateSigningRequestTPMVerified
	ConditionTypeDeviceDecommissioning                = v1beta1.ConditionTypeDeviceDecommissioning
	ConditionTypeDeviceMultipleOwners                 = v1beta1.ConditionTypeDeviceMultipleOwners
	ConditionTypeDeviceSpecValid                      = v1beta1.ConditionTypeDeviceSpecValid
	ConditionTypeDeviceUpdating                       = v1beta1.ConditionTypeDeviceUpdating
	ConditionTypeEnrollmentRequestApproved            = v1beta1.ConditionTypeEnrollmentRequestApproved
	ConditionTypeEnrollmentRequestTPMVerified         = v1beta1.ConditionTypeEnrollmentRequestTPMVerified
	ConditionTypeFleetRolloutInProgress               = v1beta1.ConditionTypeFleetRolloutInProgress
	ConditionTypeFleetValid                           = v1beta1.ConditionTypeFleetValid
	ConditionTypeRepositoryAccessible                 = v1beta1.ConditionTypeRepositoryAccessible
	ConditionTypeResourceSyncAccessible               = v1beta1.ConditionTypeResourceSyncAccessible
	ConditionTypeResourceSyncResourceParsed           = v1beta1.ConditionTypeResourceSyncResourceParsed
	ConditionTypeResourceSyncSynced                   = v1beta1.ConditionTypeResourceSyncSynced
)

// Condition utilities (re-exported)
var (
	SetStatusCondition               = v1beta1.SetStatusCondition
	FindStatusCondition              = v1beta1.FindStatusCondition
	RemoveStatusCondition            = v1beta1.RemoveStatusCondition
	IsStatusConditionTrue            = v1beta1.IsStatusConditionTrue
	IsStatusConditionFalse           = v1beta1.IsStatusConditionFalse
	SetStatusConditionByError        = v1beta1.SetStatusConditionByError
	IsStatusConditionPresentAndEqual = v1beta1.IsStatusConditionPresentAndEqual
)

// ========== Selectors ==========

type LabelSelector = v1beta1.LabelSelector
type MatchExpression = v1beta1.MatchExpression
type MatchExpressionOperator = v1beta1.MatchExpressionOperator
type MatchExpressions = v1beta1.MatchExpressions
type LabelList = v1beta1.LabelList

const (
	Exists       = v1beta1.Exists
	DoesNotExist = v1beta1.DoesNotExist
	In           = v1beta1.In
	NotIn        = v1beta1.NotIn
)

// ========== Permissions ==========

type Permission = v1beta1.Permission
type PermissionList = v1beta1.PermissionList

// ========== Primitives ==========

type Duration = v1beta1.Duration
type Percentage = v1beta1.Percentage
type CronExpression = v1beta1.CronExpression
type TimeZone = v1beta1.TimeZone

// ========== Resource Kinds ==========

type ResourceKind = v1beta1.ResourceKind

const (
	ResourceKindAuthProvider                           = v1beta1.ResourceKindAuthProvider
	ResourceKindCatalog                   ResourceKind = "Catalog" // v1alpha1-only resource
	ResourceKindCertificateSigningRequest              = v1beta1.ResourceKindCertificateSigningRequest
	ResourceKindDevice                                 = v1beta1.ResourceKindDevice
	ResourceKindEnrollmentRequest                      = v1beta1.ResourceKindEnrollmentRequest
	ResourceKindFleet                                  = v1beta1.ResourceKindFleet
	ResourceKindRepository                             = v1beta1.ResourceKindRepository
	ResourceKindResourceSync                           = v1beta1.ResourceKindResourceSync
	ResourceKindTemplateVersion                        = v1beta1.ResourceKindTemplateVersion
)

// ========== Interfaces ==========

type SensitiveDataHider = v1beta1.SensitiveDataHider

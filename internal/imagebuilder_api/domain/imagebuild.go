package domain

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
)

// ========== Resource Types ==========

type ImageBuild = api.ImageBuild
type ImageBuildList = api.ImageBuildList

// ========== Spec Types ==========

type ImageBuildSpec = api.ImageBuildSpec
type ImageBuildSource = api.ImageBuildSource
type ImageBuildDestination = api.ImageBuildDestination
type ImageBuildBinding = api.ImageBuildBinding
type ImageBuildUserConfiguration = api.ImageBuildUserConfiguration

// ========== Status Types ==========

type ImageBuildStatus = api.ImageBuildStatus
type ImageBuildCondition = api.ImageBuildCondition
type ImageBuildConditionType = api.ImageBuildConditionType
type ImageBuildConditionReason = api.ImageBuildConditionReason

// ========== Binding Types ==========

type BindingType = api.BindingType
type EarlyBinding = api.EarlyBinding
type EarlyBindingType = api.EarlyBindingType
type LateBinding = api.LateBinding
type LateBindingType = api.LateBindingType

// ========== Binding Constants ==========

const (
	BindingTypeEarly = api.BindingTypeEarly
	BindingTypeLate  = api.BindingTypeLate
	Early            = api.Early
	Late             = api.Late
)

// ========== Condition Type Constants ==========

const (
	ImageBuildConditionTypeReady = api.ImageBuildConditionTypeReady
)

// ========== Condition Reason Constants ==========

const (
	ImageBuildConditionReasonPending   = api.ImageBuildConditionReasonPending
	ImageBuildConditionReasonBuilding  = api.ImageBuildConditionReasonBuilding
	ImageBuildConditionReasonPushing   = api.ImageBuildConditionReasonPushing
	ImageBuildConditionReasonCompleted = api.ImageBuildConditionReasonCompleted
	ImageBuildConditionReasonFailed    = api.ImageBuildConditionReasonFailed
	ImageBuildConditionReasonCanceling = api.ImageBuildConditionReasonCanceling
	ImageBuildConditionReasonCanceled  = api.ImageBuildConditionReasonCanceled
)

// ========== API Parameters ==========

type ListImageBuildsParams = api.ListImageBuildsParams
type GetImageBuildParams = api.GetImageBuildParams
type GetImageBuildLogParams = api.GetImageBuildLogParams

// ========== Resource Kind ==========

type ResourceKind = api.ResourceKind

const (
	ResourceKindImageBuild = api.ResourceKindImageBuild
)

// ========== List Kind ==========

const (
	ImageBuildListKind = api.ImageBuildListKind
)

// ========== API Version ==========

const (
	APIGroup             = api.APIGroup
	ImageBuildAPIVersion = api.ImageBuildAPIVersion
)

// ========== Log Streaming ==========

const (
	// LogStreamCompleteMarker is sent by the server when a log stream is complete.
	LogStreamCompleteMarker = api.LogStreamCompleteMarker
)

// ========== Helper Functions ==========

// FindImageBuildStatusCondition finds a condition by type in the given slice
func FindImageBuildStatusCondition(conditions []ImageBuildCondition, conditionType ImageBuildConditionType) *ImageBuildCondition {
	return api.FindImageBuildStatusCondition(conditions, conditionType)
}

// SetImageBuildStatusCondition sets the corresponding condition in conditions to newCondition
func SetImageBuildStatusCondition(conditions *[]ImageBuildCondition, newCondition ImageBuildCondition) bool {
	return api.SetImageBuildStatusCondition(conditions, newCondition)
}

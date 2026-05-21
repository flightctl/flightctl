package domain

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
)

// ========== Resource Types ==========

type ImagePromotion = api.ImagePromotion
type ImagePromotionList = api.ImagePromotionList

// ========== Spec Types ==========

type ImagePromotionSpec = api.ImagePromotionSpec
type ImagePromotionSource = api.ImagePromotionSource
type ImagePromotionTarget = api.ImagePromotionTarget
type ImagePromotionTargetBase = api.ImagePromotionTargetBase
type NewCatalogItemTarget = api.NewCatalogItemTarget
type ExistingCatalogItemTarget = api.ExistingCatalogItemTarget
type ImagePromotionTargetType = api.ImagePromotionTargetType

// ========== Status Types ==========

type ImagePromotionStatus = api.ImagePromotionStatus
type ImagePromotionCondition = api.ImagePromotionCondition
type ImagePromotionConditionType = api.ImagePromotionConditionType
type ImagePromotionConditionReason = api.ImagePromotionConditionReason
type ArtifactPromotionStatus = api.ArtifactPromotionStatus

// ========== Target Type Constants ==========

const (
	ImagePromotionTargetTypeNewCatalogItem      = api.ImagePromotionTargetTypeNewCatalogItem
	ImagePromotionTargetTypeExistingCatalogItem = api.ImagePromotionTargetTypeExistingCatalogItem
)

// ========== Condition Type Constants ==========

const (
	ImagePromotionConditionTypeReady = api.ImagePromotionConditionTypeReady
)

// ========== Condition Reason Constants ==========

const (
	ImagePromotionConditionReasonWaitingForArtifacts = api.ImagePromotionConditionReasonWaitingForArtifacts
	ImagePromotionConditionReasonPublishing          = api.ImagePromotionConditionReasonPublishing
	ImagePromotionConditionReasonCompleted           = api.ImagePromotionConditionReasonCompleted
	ImagePromotionConditionReasonFailed              = api.ImagePromotionConditionReasonFailed
	ImagePromotionConditionReasonBuildFailed         = api.ImagePromotionConditionReasonBuildFailed
	ImagePromotionConditionReasonBuildCanceled       = api.ImagePromotionConditionReasonBuildCanceled
	ImagePromotionConditionReasonAmendmentFailed     = api.ImagePromotionConditionReasonAmendmentFailed
)

// ========== API Parameters ==========

type ListImagePromotionsParams = api.ListImagePromotionsParams

// ========== Resource Kind ==========

const (
	ResourceKindImagePromotion = api.ResourceKindImagePromotion
)

// ========== List Kind ==========

const (
	ImagePromotionListKind = api.ImagePromotionListKind
)

// ========== API Version ==========

const (
	ImagePromotionAPIVersion = api.ImagePromotionAPIVersion
)

// ========== Helper Functions ==========

// FindImagePromotionStatusCondition finds a condition by type in the given slice
func FindImagePromotionStatusCondition(conditions []ImagePromotionCondition, conditionType ImagePromotionConditionType) *ImagePromotionCondition {
	return api.FindImagePromotionStatusCondition(conditions, conditionType)
}

// SetImagePromotionStatusCondition sets the corresponding condition in conditions to newCondition
func SetImagePromotionStatusCondition(conditions *[]ImagePromotionCondition, newCondition ImagePromotionCondition) bool {
	return api.SetImagePromotionStatusCondition(conditions, newCondition)
}

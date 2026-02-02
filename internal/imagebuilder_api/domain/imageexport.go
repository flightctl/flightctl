package domain

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
)

// ========== Resource Types ==========

type ImageExport = api.ImageExport
type ImageExportList = api.ImageExportList

// ========== Spec Types ==========

type ImageExportSpec = api.ImageExportSpec
type ImageExportSource = api.ImageExportSource
type ExportFormatType = api.ExportFormatType

// ========== Source Types ==========

type ImageBuildRefSource = api.ImageBuildRefSource
type ImageBuildRefSourceType = api.ImageBuildRefSourceType
type ImageExportSourceType = api.ImageExportSourceType

// ========== Status Types ==========

type ImageExportStatus = api.ImageExportStatus
type ImageExportCondition = api.ImageExportCondition
type ImageExportConditionType = api.ImageExportConditionType
type ImageExportConditionReason = api.ImageExportConditionReason
type ImageExportFormatPhase = api.ImageExportFormatPhase

// ========== Export Format Constants ==========

const (
	ExportFormatTypeISO                = api.ExportFormatTypeISO
	ExportFormatTypeQCOW2              = api.ExportFormatTypeQCOW2
	ExportFormatTypeQCOW2DiskContainer = api.ExportFormatTypeQCOW2DiskContainer
	ExportFormatTypeVMDK               = api.ExportFormatTypeVMDK
)

// ========== Source Type Constants ==========

const (
	ImageBuildRefSourceTypeImageBuild = api.ImageBuildRefSourceTypeImageBuild
	ImageExportSourceTypeImageBuild   = api.ImageExportSourceTypeImageBuild
)

// ========== Condition Type Constants ==========

const (
	ImageExportConditionTypeReady = api.ImageExportConditionTypeReady
)

// ========== Condition Reason Constants ==========

const (
	ImageExportConditionReasonPending    = api.ImageExportConditionReasonPending
	ImageExportConditionReasonConverting = api.ImageExportConditionReasonConverting
	ImageExportConditionReasonPushing    = api.ImageExportConditionReasonPushing
	ImageExportConditionReasonCompleted  = api.ImageExportConditionReasonCompleted
	ImageExportConditionReasonFailed     = api.ImageExportConditionReasonFailed
	ImageExportConditionReasonCanceling  = api.ImageExportConditionReasonCanceling
	ImageExportConditionReasonCanceled   = api.ImageExportConditionReasonCanceled
)

// ========== Format Phase Constants ==========

const (
	ImageExportFormatPhaseQueued     = api.ImageExportFormatPhaseQueued
	ImageExportFormatPhaseConverting = api.ImageExportFormatPhaseConverting
	ImageExportFormatPhasePushing    = api.ImageExportFormatPhasePushing
	ImageExportFormatPhaseComplete   = api.ImageExportFormatPhaseComplete
	ImageExportFormatPhaseFailed     = api.ImageExportFormatPhaseFailed
)

// ========== API Parameters ==========

type ListImageExportsParams = api.ListImageExportsParams
type GetImageExportLogParams = api.GetImageExportLogParams

// ========== Resource Kind ==========

const (
	ResourceKindImageExport = api.ResourceKindImageExport
)

// ========== List Kind ==========

const (
	ImageExportListKind = api.ImageExportListKind
)

// ========== API Version ==========

const (
	ImageExportAPIVersion = api.ImageExportAPIVersion
)

// ========== Helper Functions ==========

// FindImageExportStatusCondition finds a condition by type in the given slice
func FindImageExportStatusCondition(conditions []ImageExportCondition, conditionType ImageExportConditionType) *ImageExportCondition {
	return api.FindImageExportStatusCondition(conditions, conditionType)
}

// SetImageExportStatusCondition sets the corresponding condition in conditions to newCondition
func SetImageExportStatusCondition(conditions *[]ImageExportCondition, newCondition ImageExportCondition) bool {
	return api.SetImageExportStatusCondition(conditions, newCondition)
}

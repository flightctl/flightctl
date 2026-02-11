package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Resource Types ==========

type ResourceSync = v1beta1.ResourceSync
type ResourceSyncList = v1beta1.ResourceSyncList
type ResourceSyncSpec = v1beta1.ResourceSyncSpec
type ResourceSyncStatus = v1beta1.ResourceSyncStatus

// ========== Event Details ==========

type ResourceSyncCompletedDetails = v1beta1.ResourceSyncCompletedDetails
type ResourceSyncCompletedDetailsDetailType = v1beta1.ResourceSyncCompletedDetailsDetailType

const (
	ResourceSyncCompleted = v1beta1.ResourceSyncCompleted
)

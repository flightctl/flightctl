package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Resource Types ==========

type Fleet = v1beta1.Fleet
type FleetList = v1beta1.FleetList
type FleetSpec = v1beta1.FleetSpec
type FleetStatus = v1beta1.FleetStatus

// ========== Rollout Types ==========

type RolloutPolicy = v1beta1.RolloutPolicy
type RolloutDeviceSelection = v1beta1.RolloutDeviceSelection
type RolloutStrategy = v1beta1.RolloutStrategy
type FleetRolloutStatus = v1beta1.FleetRolloutStatus
type Batch = v1beta1.Batch
type BatchSequence = v1beta1.BatchSequence
type Batch_Limit = v1beta1.Batch_Limit
type BatchLimit1 = v1beta1.BatchLimit1
type DisruptionBudget = v1beta1.DisruptionBudget

// ========== Rollout Strategy Constants ==========

const (
	RolloutStrategyBatchSequence = v1beta1.RolloutStrategyBatchSequence
)

// ========== Fleet Event Details Types ==========

type FleetRolloutBatchCompletedDetails = v1beta1.FleetRolloutBatchCompletedDetails
type FleetRolloutBatchCompletedDetailsDetailType = v1beta1.FleetRolloutBatchCompletedDetailsDetailType
type FleetRolloutBatchDispatchedDetails = v1beta1.FleetRolloutBatchDispatchedDetails
type FleetRolloutBatchDispatchedDetailsDetailType = v1beta1.FleetRolloutBatchDispatchedDetailsDetailType
type FleetRolloutCompletedDetails = v1beta1.FleetRolloutCompletedDetails
type FleetRolloutCompletedDetailsDetailType = v1beta1.FleetRolloutCompletedDetailsDetailType
type FleetRolloutDeviceSelectedDetails = v1beta1.FleetRolloutDeviceSelectedDetails
type FleetRolloutDeviceSelectedDetailsDetailType = v1beta1.FleetRolloutDeviceSelectedDetailsDetailType
type FleetRolloutFailedDetails = v1beta1.FleetRolloutFailedDetails
type FleetRolloutFailedDetailsDetailType = v1beta1.FleetRolloutFailedDetailsDetailType
type FleetRolloutStartedDetails = v1beta1.FleetRolloutStartedDetails
type FleetRolloutStartedDetailsDetailType = v1beta1.FleetRolloutStartedDetailsDetailType
type FleetRolloutStartedDetailsRolloutStrategy = v1beta1.FleetRolloutStartedDetailsRolloutStrategy

const (
	FleetRolloutBatchCompleted  = v1beta1.FleetRolloutBatchCompleted
	FleetRolloutBatchDispatched = v1beta1.FleetRolloutBatchDispatched
	FleetRolloutCompleted       = v1beta1.FleetRolloutCompleted
	FleetRolloutDeviceSelected  = v1beta1.FleetRolloutDeviceSelected
	FleetRolloutFailed          = v1beta1.FleetRolloutFailed
	FleetRolloutStarted         = v1beta1.FleetRolloutStarted
	FleetRolloutStrategyBatched = v1beta1.Batched
	FleetRolloutStrategyNone    = v1beta1.None

	// Direct aliases for compatibility
	Batched = v1beta1.Batched
	None    = v1beta1.None
)

// ========== Rollout Types ==========

type RolloutBatchCompletionReport = v1beta1.RolloutBatchCompletionReport

// ========== Utility Functions ==========

var FleetSpecsAreEqual = v1beta1.FleetSpecsAreEqual

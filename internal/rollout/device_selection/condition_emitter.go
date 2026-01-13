package device_selection

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
)

type conditionEmitter struct {
	orgId          uuid.UUID
	fleetName      string
	batchName      string
	serviceHandler service.Service
}

func newConditionEmitter(orgId uuid.UUID, fleetName, batchName string, serviceHandler service.Service) *conditionEmitter {
	return &conditionEmitter{
		orgId:          orgId,
		fleetName:      fleetName,
		batchName:      batchName,
		serviceHandler: serviceHandler,
	}
}

func (c *conditionEmitter) create(status domain.ConditionStatus, reason, message string) domain.Condition {
	return domain.Condition{
		Message: message,
		Reason:  reason,
		Status:  status,
		Type:    domain.ConditionTypeFleetRolloutInProgress,
	}
}

func (c *conditionEmitter) save(ctx context.Context, condition domain.Condition) error {
	return service.ApiStatusToErr(c.serviceHandler.UpdateFleetConditions(ctx, c.orgId, c.fleetName, []domain.Condition{condition}))
}

func (c *conditionEmitter) inactive(ctx context.Context) error {
	return c.save(ctx, c.create(
		domain.ConditionStatusFalse,
		domain.RolloutInactiveReason,
		"Rollout is not in progress",
	))
}

func (c *conditionEmitter) active(ctx context.Context) error {
	return c.save(ctx, c.create(
		domain.ConditionStatusTrue,
		domain.RolloutActiveReason,
		fmt.Sprintf("Rolling out %s", c.batchName),
	))
}

func (c *conditionEmitter) suspended(ctx context.Context, threshold int, completionReport domain.RolloutBatchCompletionReport) error {
	return c.save(ctx, c.create(
		domain.ConditionStatusFalse,
		domain.RolloutSuspendedReason,
		fmt.Sprintf("%s failed: %d%% of batch devices were updated successfully, while success threshold was set to %d%%; Breakdown: total=%d successful=%d failed=%d timed out=%d",
			completionReport.BatchName, completionReport.SuccessPercentage, threshold, completionReport.Total, completionReport.Successful, completionReport.Failed, completionReport.TimedOut),
	))
}

func (c *conditionEmitter) waiting(ctx context.Context) error {
	return c.save(ctx, c.create(
		domain.ConditionStatusFalse,
		domain.RolloutWaitingReason,
		fmt.Sprintf("Waiting for %s to be approved", c.batchName),
	))
}

package device_selection

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
)

type conditionEmitter struct {
	orgId          uuid.UUID
	fleetName      string
	batchName      string
	serviceHandler *service.ServiceHandler
}

func newConditionEmitter(orgId uuid.UUID, fleetName, batchName string, serviceHandler *service.ServiceHandler) *conditionEmitter {
	return &conditionEmitter{
		orgId:          orgId,
		fleetName:      fleetName,
		batchName:      batchName,
		serviceHandler: serviceHandler,
	}
}

func (c *conditionEmitter) create(status api.ConditionStatus, reason, message string) api.Condition {
	return api.Condition{
		Message: message,
		Reason:  reason,
		Status:  status,
		Type:    api.ConditionTypeFleetRolloutInProgress,
	}
}

func (c *conditionEmitter) save(ctx context.Context, condition api.Condition) error {
	return service.ApiStatusToErr(c.serviceHandler.UpdateFleetConditions(ctx, c.fleetName, []api.Condition{condition}))
}

func (c *conditionEmitter) inactive(ctx context.Context) error {
	return c.save(ctx, c.create(
		api.ConditionStatusFalse,
		api.RolloutInactiveReason,
		"Rollout is not in progress",
	))
}

func (c *conditionEmitter) active(ctx context.Context) error {
	return c.save(ctx, c.create(
		api.ConditionStatusTrue,
		api.RolloutActiveReason,
		fmt.Sprintf("Rolling out %s", c.batchName),
	))
}

func (c *conditionEmitter) suspended(ctx context.Context, threshold int, completionReport CompletionReport) error {
	return c.save(ctx, c.create(
		api.ConditionStatusFalse,
		api.RolloutSuspendedReason,
		fmt.Sprintf("%s failed: %d%% of batch devices were updated successfully, while success threshold was set to %d%%; Breakdown: total=%d successful=%d failed=%d timed out=%d",
			completionReport.BatchName, completionReport.SuccessPercentage, threshold, completionReport.Total, completionReport.Successful, completionReport.Failed, completionReport.TimedOut),
	))
}

func (c *conditionEmitter) waiting(ctx context.Context) error {
	return c.save(ctx, c.create(
		api.ConditionStatusFalse,
		api.RolloutWaitingReason,
		fmt.Sprintf("Waiting for %s to be approved", c.batchName),
	))
}

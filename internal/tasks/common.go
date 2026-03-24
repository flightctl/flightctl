package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	ItemsPerPage           = 1000
	EventProcessingTimeout = 10 * time.Second
	AckTimeout             = 5 * time.Second
	// TaskStepDefaultTimeout is the default time budget for a single long-running step
	// (e.g. one paginated list page or bulk update) when using WithTimeoutIgnoringParentDeadline.
	TaskStepDefaultTimeout = time.Minute
	// FleetValidateTaskTimeout is the time budget for the entire fleet_validate task
	// (CreateNewTemplateVersionIfFleetValid), including all config validation and store calls.
	FleetValidateTaskTimeout = 2 * time.Minute
)

// WithTimeoutIgnoringParentDeadline returns a context whose deadline comes only from timeout.
// The parent's deadline does not shorten the operation; explicit parent cancellation
// (not deadline) still ends the operation by canceling the child.
func WithTimeoutIgnoringParentDeadline(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(parent)
	iterCtx, cancelIter := context.WithTimeout(base, timeout)
	go func() {
		select {
		case <-parent.Done():
			if errors.Is(parent.Err(), context.Canceled) {
				cancelIter()
			}
		case <-iterCtx.Done():
		}
	}()
	return iterCtx, cancelIter
}

var (
	ErrUnknownConfigName      = errors.New("failed to find configuration item name")
	ErrUnknownApplicationType = errors.New("unknown application type")
)

func getOwnerFleet(device *domain.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != domain.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}

func EmitInternalTaskFailedEvent(ctx context.Context, orgID uuid.UUID, errorMessage string, originalEvent domain.Event, serviceHandler service.Service) {
	resourceKind := domain.ResourceKind(originalEvent.InvolvedObject.Kind)
	resourceName := originalEvent.InvolvedObject.Name
	reason := originalEvent.Reason
	message := fmt.Sprintf("%s internal task failed: %s - %s.", resourceKind, reason, errorMessage)
	event := domain.GetBaseEvent(ctx,
		resourceKind,
		resourceName,
		domain.EventReasonInternalTaskFailed,
		message,
		nil)

	details := domain.EventDetails{}
	if detailsErr := details.FromInternalTaskFailedDetails(domain.InternalTaskFailedDetails{
		ErrorMessage:  errorMessage,
		RetryCount:    nil,
		OriginalEvent: originalEvent,
	}); detailsErr == nil {
		event.Details = &details
	}

	// Emit the event
	serviceHandler.CreateEvent(ctx, orgID, event)
}

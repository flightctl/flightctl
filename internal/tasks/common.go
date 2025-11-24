package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	ItemsPerPage           = 1000
	EventProcessingTimeout = 10 * time.Second
	AckTimeout             = 5 * time.Second
)

var (
	ErrUnknownConfigName      = errors.New("failed to find configuration item name")
	ErrUnknownApplicationType = errors.New("unknown application type")
)

func getOwnerFleet(device *api.Device) (string, bool, error) {
	if device.Metadata.Owner == nil {
		return "", true, nil
	}

	ownerType, ownerName, err := util.GetResourceOwner(device.Metadata.Owner)
	if err != nil {
		return "", false, err
	}

	if ownerType != api.FleetKind {
		return "", false, nil
	}

	return ownerName, true, nil
}

func EmitInternalTaskFailedEvent(ctx context.Context, orgID uuid.UUID, errorMessage string, originalEvent api.Event, serviceHandler service.Service) {
	resourceKind := api.ResourceKind(originalEvent.InvolvedObject.Kind)
	resourceName := originalEvent.InvolvedObject.Name
	reason := originalEvent.Reason
	message := fmt.Sprintf("%s internal task failed: %s - %s.", resourceKind, reason, errorMessage)
	event := api.GetBaseEvent(ctx,
		resourceKind,
		resourceName,
		api.EventReasonInternalTaskFailed,
		message,
		nil)

	details := api.EventDetails{}
	if detailsErr := details.FromInternalTaskFailedDetails(api.InternalTaskFailedDetails{
		ErrorMessage:  errorMessage,
		RetryCount:    nil,
		OriginalEvent: originalEvent,
	}); detailsErr == nil {
		event.Details = &details
	}

	// Emit the event
	serviceHandler.CreateEvent(ctx, orgID, event)
}

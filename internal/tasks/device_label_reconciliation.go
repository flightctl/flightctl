package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// DeviceLabelReconciliationLogic reconciles one device using current persisted state.
type DeviceLabelReconciliationLogic struct {
	log     logrus.FieldLogger
	service labelsyncmappingservice.Service
	orgID   uuid.UUID
	event   domain.Event
}

// NewDeviceLabelReconciliationLogic builds worker logic from an event's resource identity.
func NewDeviceLabelReconciliationLogic(log logrus.FieldLogger, service labelsyncmappingservice.Service, orgID uuid.UUID, event domain.Event) (DeviceLabelReconciliationLogic, error) {
	if service == nil {
		return DeviceLabelReconciliationLogic{}, errors.New("device label reconciliation is not configured")
	}
	if log == nil {
		log = logrus.New()
	}
	return DeviceLabelReconciliationLogic{
		log:     log,
		service: service,
		orgID:   orgID,
		event:   event,
	}, nil
}

func (l DeviceLabelReconciliationLogic) Reconcile(ctx context.Context) error {
	if l.event.InvolvedObject.Kind != domain.DeviceKind {
		return fmt.Errorf("device label reconciliation called with unexpected kind %s", l.event.InvolvedObject.Kind)
	}
	deviceName := l.event.InvolvedObject.Name
	l.log.Infof("Reconciling device labels from current state for %s/%s", l.orgID, deviceName)
	result, err := l.service.ReconcileDeviceLabels(ctx, l.orgID, deviceName)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) && len(result.MappingOutcomes) == 0 {
			l.log.Infof("Skipping label reconciliation for missing device %s/%s", l.orgID, deviceName)
			return nil
		}
		return fmt.Errorf("reconcile device labels for %s/%s: %w", l.orgID, deviceName, err)
	}
	return nil
}

func shouldReconcileDeviceLabels(_ context.Context, event domain.Event) bool {
	if event.InvolvedObject.Kind != domain.DeviceKind {
		return false
	}
	if event.Reason == domain.EventReasonResourceCreated {
		return true
	}
	return event.Reason == domain.EventReasonResourceUpdated && event.Details == nil
}

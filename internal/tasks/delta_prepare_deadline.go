package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const DeltaPrepareDeadlinePollingInterval = time.Minute

type prepareDeadlineStore interface {
	ListWaitingPastDeadline(ctx context.Context) ([]model.DeltaPrepare, error)
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
}

type DeltaPrepareDeadline struct {
	log        logrus.FieldLogger
	deltaStore prepareDeadlineStore
	fleetSvc   fleetservice.Service
	deviceSvc  deviceservice.Service
	tvSvc      templateversionservice.Service
	eventSvc   eventservice.Service
}

func NewDeltaPrepareDeadline(log logrus.FieldLogger, deltaStore deltastore.Store, fleetSvc fleetservice.Service, deviceSvc deviceservice.Service, tvSvc templateversionservice.Service, eventSvc eventservice.Service) *DeltaPrepareDeadline {
	return &DeltaPrepareDeadline{
		log:        log,
		deltaStore: deltaStore,
		fleetSvc:   fleetSvc,
		deviceSvc:  deviceSvc,
		tvSvc:      tvSvc,
		eventSvc:   eventSvc,
	}
}

func (t *DeltaPrepareDeadline) Poll(ctx context.Context) {
	t.log.Info("Running DeltaPrepareDeadline Polling")
	rows, err := t.deltaStore.ListWaitingPastDeadline(ctx)
	if err != nil {
		t.log.WithError(err).Error("listing waiting delta prepares past deadline")
		return
	}
	for i := range rows {
		if err := t.failExpired(ctx, &rows[i]); err != nil {
			t.log.WithError(err).Errorf("failing expired delta prepare %s", rows[i].ID)
		}
	}
}

func (t *DeltaPrepareDeadline) failExpired(ctx context.Context, prep *model.DeltaPrepare) error {
	if err := t.deltaStore.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailed); err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return nil
		}
		return err
	}
	matches, err := t.identityMatches(ctx, prep)
	if err != nil {
		return err
	}
	if !matches {
		return nil
	}
	if err := t.clearPreparing(ctx, prep); err != nil {
		return err
	}
	return t.emitResume(ctx, prep)
}

func (t *DeltaPrepareDeadline) identityMatches(ctx context.Context, prep *model.DeltaPrepare) (bool, error) {
	switch prep.Kind {
	case domain.FleetKind:
		if t.tvSvc == nil {
			return false, fmt.Errorf("template version service is required")
		}
		tv, status := t.tvSvc.GetLatestTemplateVersion(ctx, prep.OrgID, prep.Name)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusNotFound {
				return false, nil
			}
			return false, fmt.Errorf("getting latest template version for fleet %s: %s", prep.Name, status.Message)
		}
		return equalStringPtr(prep.TemplateVersion, tv.Metadata.Name), nil
	case domain.DeviceKind:
		device, status := t.deviceSvc.GetDevice(ctx, prep.OrgID, prep.Name)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusNotFound {
				return false, nil
			}
			return false, fmt.Errorf("getting device %s: %s", prep.Name, status.Message)
		}
		return equalInt64Ptr(prep.SpecResourceVersion, device.Metadata.Generation), nil
	default:
		return false, fmt.Errorf("unsupported prepare kind %q", prep.Kind)
	}
}

func (t *DeltaPrepareDeadline) emitResume(ctx context.Context, prep *model.DeltaPrepare) error {
	switch prep.Kind {
	case domain.FleetKind:
		return t.emitFleetResume(ctx, prep)
	case domain.DeviceKind:
		t.eventSvc.CreateEvent(ctx, prep.OrgID, domain.GetBaseEvent(ctx, domain.DeviceKind, prep.Name, domain.EventReasonDeltaGenerationCompleted, "Delta generation completed.", nil))
		return nil
	default:
		return fmt.Errorf("unsupported prepare kind %q", prep.Kind)
	}
}

func (t *DeltaPrepareDeadline) emitFleetResume(ctx context.Context, prep *model.DeltaPrepare) error {
	fleet, status := t.fleetSvc.GetFleet(ctx, prep.OrgID, prep.Name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("getting fleet %s: %s", prep.Name, status.Message)
	}
	tv := lo.FromPtr(prep.TemplateVersion)
	if tv == "" {
		tv = lo.FromPtr(liveFleetTemplateVersion(fleet))
	}
	status = t.fleetSvc.UpdateFleetAnnotations(ctx, prep.OrgID, prep.Name, map[string]string{
		domain.FleetAnnotationTemplateVersion: tv,
	}, nil)
	if status.Code != http.StatusOK {
		return fmt.Errorf("setting fleet template version annotation: %s", status.Message)
	}
	if err := t.deviceSvc.SetOutOfDate(ctx, prep.OrgID, util.ResourceOwner(domain.FleetKind, prep.Name)); err != nil {
		return err
	}
	immediate := fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil
	t.eventSvc.CreateEvent(ctx, prep.OrgID, common.GetFleetRolloutStartedEvent(ctx, tv, prep.Name, immediate, false))
	return nil
}

func (t *DeltaPrepareDeadline) clearPreparing(ctx context.Context, prep *model.DeltaPrepare) error {
	switch prep.Kind {
	case domain.FleetKind:
		fleet, status := t.fleetSvc.GetFleet(ctx, prep.OrgID, prep.Name, domain.GetFleetParams{})
		if status.Code != http.StatusOK {
			return fmt.Errorf("getting fleet %s: %s", prep.Name, status.Message)
		}
		if fleet.Status == nil {
			return nil
		}
		domain.RemoveStatusCondition(&fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
		fleet.Status.DeltaGeneration = nil
		_, status = t.fleetSvc.ReplaceFleetStatus(ctx, prep.OrgID, prep.Name, *fleet)
		if status.Code != http.StatusOK {
			return fmt.Errorf("clearing fleet preparing status: %s", status.Message)
		}
		return nil
	case domain.DeviceKind:
		device, status := t.deviceSvc.GetDevice(ctx, prep.OrgID, prep.Name)
		if status.Code != http.StatusOK {
			return fmt.Errorf("getting device %s: %s", prep.Name, status.Message)
		}
		if device.Status == nil {
			return nil
		}
		domain.RemoveStatusCondition(&device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
		device.Status.DeltaGeneration = nil
		_, status = t.deviceSvc.ReplaceDeviceStatus(ctx, prep.OrgID, prep.Name, *device, false)
		if status.Code != http.StatusOK {
			return fmt.Errorf("clearing device preparing status: %s", status.Message)
		}
		return nil
	default:
		return fmt.Errorf("unsupported prepare kind %q", prep.Kind)
	}
}

func liveFleetTemplateVersion(fleet *domain.Fleet) *string {
	if fleet == nil || fleet.Metadata.Annotations == nil {
		return nil
	}
	tv, ok := (*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion]
	if !ok || tv == "" {
		return nil
	}
	return &tv
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const DeltaPrepareDeadlinePollingInterval = time.Minute

type prepareDeadlineStore interface {
	ListWaitingPastDeadline(ctx context.Context, limit int, asOf time.Time) ([]model.DeltaPrepare, error)
}

type prepareDeadlineStatusStore interface {
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
}

type DeltaPrepareDeadline struct {
	log        logrus.FieldLogger
	deltaStore prepareDeadlineStore
	prepareSvc deltaprepare.Service
	fleetSvc   fleetservice.Service
	deviceSvc  deviceservice.Service
	tvSvc      templateversionservice.Service
	eventSvc   eventservice.Service
}

func NewDeltaPrepareDeadline(log logrus.FieldLogger, deltaStore prepareDeadlineStore, prepareSvc deltaprepare.Service, fleetSvc fleetservice.Service, deviceSvc deviceservice.Service, tvSvc templateversionservice.Service, eventSvc eventservice.Service) *DeltaPrepareDeadline {
	return &DeltaPrepareDeadline{
		log:        log,
		deltaStore: deltaStore,
		prepareSvc: prepareSvc,
		fleetSvc:   fleetSvc,
		deviceSvc:  deviceSvc,
		tvSvc:      tvSvc,
		eventSvc:   eventSvc,
	}
}

func (t *DeltaPrepareDeadline) Poll(ctx context.Context) {
	t.log.Info("Running DeltaPrepareDeadline Polling")
	ctx, span := tracing.StartSpan(ctx, "flightctl/tasks", "DeltaPrepareDeadline.Poll")
	defer span.End()

	rows, err := t.deltaStore.ListWaitingPastDeadline(ctx, deltapreparestore.MaxListWaitingPastDeadline, time.Now())
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
	claimed, err := t.claimForFailure(ctx, prep)
	if err != nil || !claimed {
		return err
	}

	matches, err := t.identityMatches(ctx, prep)
	if err != nil {
		return err
	}
	if !matches {
		return t.markFailed(ctx, prep)
	}
	if err := t.clearPreparing(ctx, prep); err != nil {
		return err
	}
	// The identity may have changed while the status mutation was in flight.
	// Do not emit a resume event for a newer resource version.
	matches, err = t.identityMatches(ctx, prep)
	if err != nil {
		return err
	}
	if !matches {
		return t.markFailed(ctx, prep)
	}
	if err := t.emitResume(ctx, prep); err != nil {
		return err
	}
	return t.markFailed(ctx, prep)
}

// claimForFailure moves the prepare to a retryable intermediate state before
// touching the resource. A CAS miss therefore performs no side effects, while
// a process failure after the claim leaves a row that the deadline poller will
// pick up again.
func (t *DeltaPrepareDeadline) claimForFailure(ctx context.Context, prep *model.DeltaPrepare) (bool, error) {
	if prep.Status == model.DeltaPrepareFailing {
		return true, nil
	}
	var (
		updated *model.DeltaPrepare
		err     error
	)
	if t.prepareSvc != nil {
		claimed := *prep
		claimed.Status = model.DeltaPrepareFailing
		updated, err = t.prepareSvc.UpdateDeltaPrepare(ctx, prep.ResourceVersion, &claimed)
	} else if store, ok := t.deltaStore.(prepareDeadlineStatusStore); ok {
		err = store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailing)
	} else {
		return false, fmt.Errorf("delta prepare service is required")
	}
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if updated != nil {
		*prep = *updated
	} else {
		prep.Status = model.DeltaPrepareFailing
	}
	return true, nil
}

func (t *DeltaPrepareDeadline) markFailed(ctx context.Context, prep *model.DeltaPrepare) error {
	var err error
	if t.prepareSvc != nil {
		failed := *prep
		failed.Status = model.DeltaPrepareFailed
		_, err = t.prepareSvc.UpdateDeltaPrepare(ctx, prep.ResourceVersion, &failed)
	} else if store, ok := t.deltaStore.(prepareDeadlineStatusStore); ok {
		err = store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailed)
	} else {
		return fmt.Errorf("delta prepare service is required")
	}
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		return nil
	}
	return err
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
		if tv == nil {
			return false, nil
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
		if device == nil || prep.SpecHash == nil || *prep.SpecHash == "" {
			return false, nil
		}
		return device.SpecHash() == *prep.SpecHash, nil
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
	if tv == "" {
		return nil
	}
	status = t.fleetSvc.UpdateFleetAnnotations(ctx, prep.OrgID, prep.Name, map[string]string{
		domain.FleetAnnotationTemplateVersion: tv,
	}, []string{domain.FleetAnnotationDeltaPrepareResourceVersion})
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
		_, status = t.deviceSvc.ReplaceServiceOwnedStatus(ctx, prep.OrgID, prep.Name, *device)
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

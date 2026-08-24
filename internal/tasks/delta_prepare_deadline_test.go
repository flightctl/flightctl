package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type fakeDeadlineStore struct {
	waiting []model.DeltaPrepare
	casTo   map[uuid.UUID]string
}

func (f *fakeDeadlineStore) ListWaitingPastDeadline(context.Context) ([]model.DeltaPrepare, error) {
	out := make([]model.DeltaPrepare, len(f.waiting))
	copy(out, f.waiting)
	return out, nil
}

func (f *fakeDeadlineStore) CASPrepareStatus(_ context.Context, id uuid.UUID, to string) error {
	for i := range f.waiting {
		if f.waiting[i].ID != id {
			continue
		}
		if f.waiting[i].Status != model.DeltaPrepareWaiting {
			return flterrors.ErrNoRowsUpdated
		}
		f.waiting[i].Status = to
		if f.casTo == nil {
			f.casTo = map[uuid.UUID]string{}
		}
		f.casTo[id] = to
		return nil
	}
	return flterrors.ErrNoRowsUpdated
}

type deadlineEventRecorder struct {
	events []*domain.Event
}

func (e *deadlineEventRecorder) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	cp := *event
	e.events = append(e.events, &cp)
}

func (e *deadlineEventRecorder) ListEvents(context.Context, uuid.UUID, domain.ListEventsParams) (*domain.EventList, domain.Status) {
	return nil, domain.StatusOK()
}

func (e *deadlineEventRecorder) DeleteEventsOlderThan(context.Context, time.Time) (int64, domain.Status) {
	return 0, domain.StatusOK()
}

func TestDeltaPrepareDeadlinePoll(t *testing.T) {
	orgId := uuid.New()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	t.Run("When a waiting prepare is past deadline it should CAS-fail and emit FleetRolloutStarted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fleetSvc := fleetservice.NewMockService(ctrl)
		deviceSvc := deviceservice.NewMockService(ctrl)
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr("fleet-1"),
				Annotations: &map[string]string{domain.FleetAnnotationTemplateVersion: "tv-1"},
			},
			Spec:   domain.FleetSpec{},
			Status: &domain.FleetStatus{},
		}
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, "fleet-1", gomock.Any()).Return(fleet, domain.StatusOK()).AnyTimes()
		fleetSvc.EXPECT().UpdateFleetAnnotations(gomock.Any(), orgId, "fleet-1", gomock.Any(), gomock.Any()).Return(domain.StatusOK())
		fleetSvc.EXPECT().ReplaceFleetStatus(gomock.Any(), orgId, "fleet-1", gomock.Any()).Return(fleet, domain.StatusOK())
		deviceSvc.EXPECT().SetOutOfDate(gomock.Any(), orgId, gomock.Any()).Return(nil)
		rec := &deadlineEventRecorder{}
		prep := model.DeltaPrepare{
			ID:              uuid.New(),
			OrgID:           orgId,
			Kind:            domain.FleetKind,
			Name:            "fleet-1",
			TemplateVersion: lo.ToPtr("tv-1"),
			Status:          model.DeltaPrepareWaiting,
			Deadline:        lo.ToPtr(time.Now().Add(-time.Minute)),
		}
		store := &fakeDeadlineStore{waiting: []model.DeltaPrepare{prep}}
		task := &DeltaPrepareDeadline{log: log, deltaStore: store, fleetSvc: fleetSvc, deviceSvc: deviceSvc, eventSvc: rec}

		task.Poll(context.Background())
		assert.Equal(t, model.DeltaPrepareFailed, store.waiting[0].Status)
		require.Len(t, rec.events, 1)
		assert.Equal(t, domain.EventReasonFleetRolloutStarted, rec.events[0].Reason)
	})

	t.Run("When the list is empty it should not emit", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		rec := &deadlineEventRecorder{}
		task := &DeltaPrepareDeadline{
			log:        log,
			deltaStore: &fakeDeadlineStore{},
			fleetSvc:   fleetservice.NewMockService(ctrl),
			deviceSvc:  deviceservice.NewMockService(ctrl),
			eventSvc:   rec,
		}
		task.Poll(context.Background())
		assert.Empty(t, rec.events)
	})

	t.Run("When live TV is stale it should CAS-fail without emit", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fleetSvc := fleetservice.NewMockService(ctrl)
		deviceSvc := deviceservice.NewMockService(ctrl)
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr("fleet-1"),
				Annotations: &map[string]string{domain.FleetAnnotationTemplateVersion: "tv-2"},
			},
			Spec: domain.FleetSpec{},
		}
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, "fleet-1", gomock.Any()).Return(fleet, domain.StatusOK())
		rec := &deadlineEventRecorder{}
		prep := model.DeltaPrepare{
			ID:              uuid.New(),
			OrgID:           orgId,
			Kind:            domain.FleetKind,
			Name:            "fleet-1",
			TemplateVersion: lo.ToPtr("tv-1"),
			Status:          model.DeltaPrepareWaiting,
		}
		store := &fakeDeadlineStore{waiting: []model.DeltaPrepare{prep}}
		task := &DeltaPrepareDeadline{log: log, deltaStore: store, fleetSvc: fleetSvc, deviceSvc: deviceSvc, eventSvc: rec}
		task.Poll(context.Background())
		assert.Equal(t, model.DeltaPrepareFailed, store.waiting[0].Status)
		assert.Empty(t, rec.events)
	})
}

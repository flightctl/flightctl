package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// fakeEventStore is a small in-memory implementation of internal/store/event.Store.
type fakeEventStore struct {
	events    []domain.Event
	createErr error
}

func (f *fakeEventStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeEventStore) Create(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.events = append(f.events, *event)
	return nil
}

func (f *fakeEventStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EventList, error) {
	return &domain.EventList{Items: f.events}, nil
}

func (f *fakeEventStore) DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error) {
	return 0, nil
}

// fakeWorkerClient records every EmitEvent call for assertions.
type fakeWorkerClient struct {
	emitted []*domain.Event
}

func (f *fakeWorkerClient) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	f.emitted = append(f.emitted, event)
}

func newTestHandler() (*ServiceHandler, *fakeEventStore, *fakeWorkerClient) {
	fakeStore := &fakeEventStore{}
	fakeWorker := &fakeWorkerClient{}
	h := NewServiceHandler(fakeStore, fakeWorker, logrus.New())
	return h, fakeStore, fakeWorker
}

func compareEventReasons(t *testing.T, expected []domain.EventReason, events []domain.Event) {
	t.Helper()
	require.Len(t, events, len(expected))
	for i, event := range events {
		require.Equal(t, expected[i], event.Reason)
	}
}

func TestCreateEvent(t *testing.T) {
	t.Run("When event is nil it should no-op", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		h.CreateEvent(context.Background(), uuid.New(), nil)
		require.Empty(t, fakeStore.events)
		require.Empty(t, fakeWorker.emitted)
	})

	t.Run("When the store succeeds it should persist the event and notify the worker client", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Len(t, fakeStore.events, 1)
		require.Len(t, fakeWorker.emitted, 1)
	})

	t.Run("When the store fails it should not notify the worker client", func(t *testing.T) {
		h, fakeStore, fakeWorker := newTestHandler()
		fakeStore.createErr = errors.New("db down")
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Empty(t, fakeStore.events)
		require.Empty(t, fakeWorker.emitted)
	})

	t.Run("When the workerClient is nil it should still persist the event", func(t *testing.T) {
		fakeStore := &fakeEventStore{}
		h := NewServiceHandler(fakeStore, nil, logrus.New())
		orgId := uuid.New()
		event := domain.GetBaseEvent(context.Background(), domain.DeviceKind, "dev1", domain.EventReasonResourceCreated, "created", nil)
		h.CreateEvent(context.Background(), orgId, event)
		require.Len(t, fakeStore.events, 1)
	})
}

func TestHandleGenericResourceDeletedEvents(t *testing.T) {
	t.Run("When err is nil it should emit a deletion-success event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleGenericResourceDeletedEvents(context.Background(), domain.FleetKind, uuid.New(), "f1", nil, nil, false, nil)
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceDeleted}, fakeStore.events)
	})

	t.Run("When err is non-nil it should emit a deletion-failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleGenericResourceDeletedEvents(context.Background(), domain.FleetKind, uuid.New(), "f1", nil, nil, false, errors.New("boom"))
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceDeletionFailed}, fakeStore.events)
	})
}

func prepareTestDevice(name string) *domain.Device {
	status := domain.NewDeviceStatus()
	return &domain.Device{
		ApiVersion: "v1beta1",
		Kind:       "Device",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name), Labels: &map[string]string{"labelKey": "labelValue"}},
		Spec:       &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		Status:     &status,
	}
}

func TestHandleDeviceUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a creation/update failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleDeviceUpdatedEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", nil, nil, true, errors.New("boom"))
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceCreationFailed}, fakeStore.events)
	})

	t.Run("When created is true it should emit a single resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		device := prepareTestDevice("dev1")
		h.HandleDeviceUpdatedEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", nil, device, true, nil)
		compareEventReasons(t, []domain.EventReason{domain.EventReasonResourceCreated}, fakeStore.events)
	})

	t.Run("When updated with an empty old device it should not panic and should emit a status event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		device := prepareTestDevice("dev1")
		device.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
		oldDevice := &domain.Device{}
		require.NotPanics(t, func() {
			h.HandleDeviceUpdatedEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", oldDevice, device, false, nil)
		})
		require.Len(t, fakeStore.events, 1)
	})

	t.Run("When multiple status fields transition to Unknown it should deduplicate DeviceDisconnected events", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		oldDevice := prepareTestDevice("dev1")
		oldDevice.Status.Summary.Status = domain.DeviceSummaryStatusOnline
		oldDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
		oldDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusHealthy

		newDevice := prepareTestDevice("dev1")
		newDevice.Status.Summary.Status = domain.DeviceSummaryStatusUnknown
		newDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUnknown
		newDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusUnknown

		h.HandleDeviceUpdatedEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", oldDevice, newDevice, false, nil)

		count := 0
		for _, e := range fakeStore.events {
			if e.Reason == domain.EventReasonDeviceDisconnected {
				count++
			}
		}
		require.Equal(t, 1, count)
	})

	t.Run("When the resources cannot be cast to *domain.Device it should no-op", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleDeviceUpdatedEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", "not-a-device", "also-not", false, nil)
		require.Empty(t, fakeStore.events)
	})
}

func TestHandleDeviceDecommissionEvents(t *testing.T) {
	t.Run("When err is nil it should emit a decommission-success event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleDeviceDecommissionEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", nil, nil, false, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonDeviceDecommissioned, fakeStore.events[0].Reason)
	})

	t.Run("When err is non-nil it should emit a decommission-failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleDeviceDecommissionEvents(context.Background(), domain.DeviceKind, uuid.New(), "dev1", nil, nil, false, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonDeviceDecommissionFailed, fakeStore.events[0].Reason)
	})
}

func TestEmitFleetRolloutStartedEvent(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	h.EmitFleetRolloutStartedEvent(context.Background(), uuid.New(), "tv1", "fleet1", false)
	require.Len(t, fakeStore.events, 1)
}

func prepareTestFleet(name string) *domain.Fleet {
	return &domain.Fleet{
		ApiVersion: "v1beta1",
		Kind:       "Fleet",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"k": "v"}},
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{Spec: domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}}},
		},
	}
}

func TestHandleFleetUpdatedEvents(t *testing.T) {
	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fleet := prepareTestFleet("f1")
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), "f1", nil, fleet, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})

	t.Run("When the fleet-valid condition transitions to true it should emit a FleetValid event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusFalse, Reason: string(domain.EventReasonFleetInvalid)},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		h.emitFleetValidEvents(context.Background(), uuid.New(), name, oldFleet, newFleet)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonFleetValid, fakeStore.events[0].Reason)
	})

	t.Run("When the fleet-valid condition transitions to false it should emit a FleetInvalid event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusFalse, Reason: string(domain.EventReasonFleetInvalid), Message: "bad config"},
			}},
		}
		h.emitFleetValidEvents(context.Background(), uuid.New(), name, oldFleet, newFleet)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonFleetInvalid, fakeStore.events[0].Reason)
	})

	t.Run("When the fleet-valid condition is unchanged it should not emit an event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("f1")},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetValid, Status: domain.ConditionStatusTrue, Reason: string(domain.EventReasonFleetValid)},
			}},
		}
		h.emitFleetValidEvents(context.Background(), uuid.New(), "f1", fleet, fleet)
		require.Empty(t, fakeStore.events)
	})

	t.Run("When the new fleet has no status it should not emit an event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("f1")}}
		h.emitFleetValidEvents(context.Background(), uuid.New(), "f1", fleet, fleet)
		require.Empty(t, fakeStore.events)
	})

	t.Run("When a new rollout annotation appears it should emit a FleetRolloutNew event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		oldFleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCreated)
	})

	t.Run("When a rollout batch completes it should emit a FleetRolloutBatchCompleted event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		report := `{"batchName":"batch-1","successPercentage":100,"total":1,"successful":1}`
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(name),
				Annotations: &map[string]string{
					domain.FleetAnnotationDeployingTemplateVersion:  "v2",
					domain.FleetAnnotationLastBatchCompletionReport: report,
				},
			},
		}
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutBatchCompleted)
	})

	t.Run("When the final rollout batch completes it should also emit a FleetRolloutCompleted event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		report := `{"batchName":"` + domain.FinalImplicitBatchName + `","successPercentage":100,"total":1,"successful":1}`
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(name),
				Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"},
			},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(name),
				Annotations: &map[string]string{
					domain.FleetAnnotationDeployingTemplateVersion:  "v2",
					domain.FleetAnnotationLastBatchCompletionReport: report,
				},
			},
		}
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutBatchCompleted)
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCompleted)
	})

	t.Run("When the rollout-in-progress condition becomes inactive it should emit a FleetRolloutCompleted event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusTrue, Reason: "Active"},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusFalse, Reason: domain.RolloutInactiveReason},
			}},
		}
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutCompleted)
	})

	t.Run("When the rollout-in-progress condition becomes suspended it should emit a FleetRolloutFailed event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "f1"
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusTrue, Reason: "Active"},
			}},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.FleetAnnotationDeployingTemplateVersion: "v2"}},
			Status: &domain.FleetStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeFleetRolloutInProgress, Status: domain.ConditionStatusFalse, Reason: domain.RolloutSuspendedReason, Message: "paused"},
			}},
		}
		h.HandleFleetUpdatedEvents(context.Background(), domain.FleetKind, uuid.New(), name, oldFleet, newFleet, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonFleetRolloutFailed)
	})
}

func TestHandleRepositoryUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleRepositoryUpdatedEvents(context.Background(), domain.RepositoryKind, uuid.New(), "r1", nil, nil, false, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceUpdateFailed, fakeStore.events[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		repo := &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("r1")}}
		h.HandleRepositoryUpdatedEvents(context.Background(), domain.RepositoryKind, uuid.New(), "r1", nil, repo, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})

	t.Run("When the Accessible condition becomes true it should emit a RepositoryAccessible event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "r1"
		oldRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.RepositoryStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeRepositoryAccessible, Status: domain.ConditionStatusFalse},
			}},
		}
		newRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.RepositoryStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeRepositoryAccessible, Status: domain.ConditionStatusTrue},
			}},
		}
		h.HandleRepositoryUpdatedEvents(context.Background(), domain.RepositoryKind, uuid.New(), name, oldRepo, newRepo, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonRepositoryAccessible)
	})
}

func TestHandleAuthProviderUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleAuthProviderUpdatedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, nil, true, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, fakeStore.events[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		ap := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1")}}
		h.HandleAuthProviderUpdatedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, ap, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})

	t.Run("When updated with metadata changes it should emit a resource-updated event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		oldAP := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(1))}}
		newAP := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(2))}}
		h.HandleAuthProviderUpdatedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", oldAP, newAP, false, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceUpdated, fakeStore.events[0].Reason)
	})

	t.Run("When updated with no metadata changes it should not emit an event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		ap := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(1))}}
		h.HandleAuthProviderUpdatedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", ap, ap, false, nil)
		require.Empty(t, fakeStore.events)
	})
}

func TestHandleAuthProviderDeletedEvents(t *testing.T) {
	t.Run("When err is nil it should emit a deletion-success event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleAuthProviderDeletedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, nil, false, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceDeleted, fakeStore.events[0].Reason)
	})

	t.Run("When err is non-nil it should emit a deletion-failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleAuthProviderDeletedEvents(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, nil, false, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceDeletionFailed, fakeStore.events[0].Reason)
	})
}

func TestHandleEnrollmentRequestUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleEnrollmentRequestUpdatedEvents(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, true, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, fakeStore.events[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		er := &domain.EnrollmentRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("er1")}}
		h.HandleEnrollmentRequestUpdatedEvents(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, er, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})
}

func TestHandleEnrollmentRequestApprovedEvents(t *testing.T) {
	t.Run("When err is nil it should emit an approved event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleEnrollmentRequestApprovedEvents(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, false, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonEnrollmentRequestApproved, fakeStore.events[0].Reason)
	})

	t.Run("When err is non-nil it should emit an approval-failed event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleEnrollmentRequestApprovedEvents(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, false, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonEnrollmentRequestApprovalFailed, fakeStore.events[0].Reason)
	})
}

func TestHandleResourceSyncUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleResourceSyncUpdatedEvents(context.Background(), domain.ResourceSyncKind, uuid.New(), "rs1", nil, nil, true, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, fakeStore.events[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		rs := &domain.ResourceSync{Metadata: domain.ObjectMeta{Name: lo.ToPtr("rs1")}}
		h.HandleResourceSyncUpdatedEvents(context.Background(), domain.ResourceSyncKind, uuid.New(), "rs1", nil, rs, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})

	t.Run("When the commit hash changes it should emit a commit-detected event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &domain.ResourceSyncStatus{ObservedCommit: lo.ToPtr("aaa")},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &domain.ResourceSyncStatus{ObservedCommit: lo.ToPtr("bbb")},
		}
		h.HandleResourceSyncUpdatedEvents(context.Background(), domain.ResourceSyncKind, uuid.New(), name, oldRs, newRs, false, nil)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncCommitDetected)
	})

	t.Run("When the Synced condition transitions to true it should emit a synced event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusFalse},
			}},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusTrue},
			}},
		}
		h.emitResourceSyncConditionEvents(context.Background(), uuid.New(), name, oldRs, newRs)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncSynced)
	})

	t.Run("When the Synced condition fails (not NewHashDetected) it should emit a sync-failed event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusTrue},
			}},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusFalse, Reason: "SomeFailure", Message: "it broke"},
			}},
		}
		h.emitResourceSyncConditionEvents(context.Background(), uuid.New(), name, oldRs, newRs)
		var reasons []domain.EventReason
		for _, e := range fakeStore.events {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncSyncFailed)
	})
}

func TestHandleCertificateSigningRequestUpdatedEvents(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		h.HandleCertificateSigningRequestUpdatedEvents(context.Background(), domain.CertificateSigningRequestKind, uuid.New(), "csr1", nil, nil, true, errors.New("boom"))
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, fakeStore.events[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		csr := &domain.CertificateSigningRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("csr1")}}
		h.HandleCertificateSigningRequestUpdatedEvents(context.Background(), domain.CertificateSigningRequestKind, uuid.New(), "csr1", nil, csr, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})
}

func TestHandleTemplateVersionUpdatedEvents(t *testing.T) {
	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		tv := &domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv1")}}
		h.HandleTemplateVersionUpdatedEvents(context.Background(), domain.TemplateVersionKind, uuid.New(), "tv1", nil, tv, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})
}

func TestHandleCatalogUpdatedEvents(t *testing.T) {
	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		catalog := &domain.Catalog{Metadata: domain.ObjectMeta{Name: lo.ToPtr("cat1")}}
		h.HandleCatalogUpdatedEvents(context.Background(), domain.CatalogKind, uuid.New(), "cat1", nil, catalog, true, nil)
		require.Len(t, fakeStore.events, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeStore.events[0].Reason)
	})
}

func TestComputeResourceUpdatedDetails(t *testing.T) {
	h, _, _ := newTestHandler()

	t.Run("When generation changes it should report a Spec update", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(1))}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(2))}
		details := h.computeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Spec)
	})

	t.Run("When labels change it should report a Labels update", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Labels: lo.ToPtr(map[string]string{"a": "1"})}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Labels: lo.ToPtr(map[string]string{"a": "2"})}
		details := h.computeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Labels)
	})

	t.Run("When owner changes it should report an Owner update with previous/new owner", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Owner: lo.ToPtr("owner1")}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Owner: lo.ToPtr("owner2")}
		details := h.computeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Owner)
		require.Equal(t, lo.ToPtr("owner1"), details.PreviousOwner)
		require.Equal(t, lo.ToPtr("owner2"), details.NewOwner)
	})

	t.Run("When nothing changes it should return nil", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(1))}
		details := h.computeResourceUpdatedDetails(old, old)
		require.Nil(t, details)
	})
}

func TestCastResources(t *testing.T) {
	t.Run("When both resources are nil it should return ok=true with nil pointers", func(t *testing.T) {
		oldTyped, newTyped, ok := castResources[domain.Device](nil, nil)
		require.True(t, ok)
		require.Nil(t, oldTyped)
		require.Nil(t, newTyped)
	})

	t.Run("When both resources are the correct type it should return ok=true", func(t *testing.T) {
		device := prepareTestDevice("dev1")
		oldTyped, newTyped, ok := castResources[domain.Device](device, device)
		require.True(t, ok)
		require.Same(t, device, oldTyped)
		require.Same(t, device, newTyped)
	})

	t.Run("When a resource has the wrong type it should return ok=false", func(t *testing.T) {
		_, _, ok := castResources[domain.Device]("not-a-device", nil)
		require.False(t, ok)
	})
}

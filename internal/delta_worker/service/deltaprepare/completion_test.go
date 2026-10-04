package deltaprepare

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type completionStatusStore struct{}

func (completionStatusStore) ResumeDeltaIfCurrent(context.Context, uuid.UUID, string, int64) (bool, error) {
	return true, nil
}

func (completionStatusStore) SetOutOfDate(context.Context, uuid.UUID, string) error { return nil }

func (completionStatusStore) Mutate(_ context.Context, _ uuid.UUID, _ string, _ *domain.Device, apply devicestore.DeviceApplyFunc, _ ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	resourceVersion := "3"
	device := &domain.Device{
		Metadata: domain.ObjectMeta{ResourceVersion: &resourceVersion},
		Status:   &domain.DeviceStatus{DeltaGeneration: &domain.DeltaGenerationStatus{}},
	}
	mutation := &devicestore.DeviceMutation{Device: device}
	if err := apply(mutation); err != nil {
		return nil, nil, false, err
	}
	return mutation.Device, device, false, nil
}

type completionEventServiceStub struct {
	events []*domain.Event
}

func (s *completionEventServiceStub) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	s.events = append(s.events, event)
}

func (*completionEventServiceStub) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func newCompletionServiceForTest(t *testing.T, store deltapreparestore.Store) *ServiceHandler {
	handler, err := NewCompletionService(
		store,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		&completionEventServiceStub{},
	)
	require.NoError(t, err)
	return handler
}

func TestResumeCompletedPrepareUsesProvidedPrepare(t *testing.T) {
	store := &recordingStore{}
	handler := newCompletionServiceForTest(t, store)

	generation := int64(7)
	prepare := &model.DeltaPrepare{
		ID:                    uuid.New(),
		Kind:                  domain.DeviceKind,
		Name:                  "device-1",
		Generation:            &generation,
		SourceResourceVersion: 3,
		Status:                model.DeltaPrepareComplete,
		ResourceVersion:       2,
	}
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "request-1")
	require.NoError(t, handler.ResumeCompletedPrepare(ctx, prepare))
	require.Zero(t, store.gets)
	events := handler.events.(*completionEventServiceStub).events
	require.Len(t, events, 1)
	require.Equal(t, domain.EventReasonDeltaGenerationCompleted, events[0].Reason)
	// The worker receives the generation after the event is serialized to the queue.
	payload, err := json.Marshal(events[0])
	require.NoError(t, err)
	var received domain.Event
	require.NoError(t, json.Unmarshal(payload, &received))
	require.NotNil(t, received.Metadata.Annotations)
	require.Equal(t, "7", (*received.Metadata.Annotations)[domain.EventAnnotationDeltaGeneration])
	require.Equal(t, "request-1", (*received.Metadata.Annotations)[domain.EventAnnotationRequestID])
}

func TestNewCompletionServiceRequiresStore(t *testing.T) {
	_, err := NewCompletionService(
		nil,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		&completionEventServiceStub{},
	)
	require.EqualError(t, err, "delta prepare store is required")
}

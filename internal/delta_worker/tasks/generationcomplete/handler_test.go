package generationcomplete

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type completionStatusStore struct{}

func (completionStatusStore) ResumeDeltaIfCurrent(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}

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
	created []*domain.Event
}

func (s *completionEventServiceStub) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	s.created = append(s.created, event)
}

func (*completionEventServiceStub) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func newCompletionServiceForTest(t *testing.T, store deltapreparestore.Store) *deltaprepare.ServiceHandler {
	handler, _ := newCompletionServiceWithEventsForTest(t, store)
	return handler
}

func newCompletionServiceWithEventsForTest(t *testing.T, store deltapreparestore.Store) (*deltaprepare.ServiceHandler, *completionEventServiceStub) {
	events := &completionEventServiceStub{}
	handler, err := deltaprepare.NewCompletionService(
		store,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		events,
	)
	require.NoError(t, err)
	return handler, events
}

type completionStore struct {
	key      *deltastore.GenerationKey
	progress []deltapreparestore.PrepareProgress
}

func (s *completionStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error {
	return nil
}

func (s *completionStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	return deltapreparestore.PrepareAdmission{}, nil
}

func (s *completionStore) GetDeltaPrepare(context.Context, deltapreparestore.PrepareKey, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (s *completionStore) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, nil
}

func (s *completionStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (s *completionStore) DecrementPendingGenerationsForGeneration(_ context.Context, key deltastore.GenerationKey) ([]deltapreparestore.PrepareProgress, error) {
	s.key = &key
	return s.progress, nil
}

var _ deltapreparestore.Store = (*completionStore)(nil)

type progressStatusStore struct {
	calls     int
	completed int64
	total     int64
}

func (s *progressStatusStore) ResumeDeltaIfCurrent(context.Context, uuid.UUID, string, string) (bool, error) {
	return false, nil
}

func (s *progressStatusStore) Mutate(_ context.Context, _ uuid.UUID, _ string, _ *domain.Device, apply devicestore.DeviceApplyFunc, _ ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	resourceVersion := "3"
	device := &domain.Device{
		Metadata: domain.ObjectMeta{ResourceVersion: &resourceVersion},
		Status:   &domain.DeviceStatus{DeltaGeneration: &domain.DeltaGenerationStatus{}},
	}
	mutation := &devicestore.DeviceMutation{Device: device}
	if err := apply(mutation); err != nil {
		return nil, nil, false, err
	}
	s.calls++
	s.completed = mutation.Device.Status.DeltaGeneration.Completed
	s.total = mutation.Device.Status.DeltaGeneration.Total
	return mutation.Device, device, false, nil
}

func TestHandlerHandle(t *testing.T) {
	orgID := uuid.New()
	generation := &model.DeltaGeneration{
		OrgID:           orgID,
		ImageRepository: "quay.io/example/os",
		SourceDigest:    "sha256:source",
		TargetDigest:    "sha256:target",
		Status:          model.DeltaGenerationSucceeded,
	}
	event, err := deltageneration.NewGenerationCompleteEvent(generation)
	require.NoError(t, err)

	store := &completionStore{}
	completion := newCompletionServiceForTest(t, store)
	handler, err := NewHandler(completion, workerservice.NewStorePreparingStatus(nil, completionStatusStore{}))
	require.NoError(t, err)
	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{OrgId: orgID, Event: *event})
	require.NoError(t, err)
	require.Equal(t, &deltastore.GenerationKey{
		OrgID:           orgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}, store.key)
}

func TestHandlerHandleInvalidPayload(t *testing.T) {
	completion := newCompletionServiceForTest(t, &completionStore{})
	handler, err := NewHandler(completion, workerservice.NewStorePreparingStatus(nil, completionStatusStore{}))
	require.NoError(t, err)
	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{
		OrgId: uuid.New(),
		Event: domain.Event{
			Reason:  domain.EventReasonDeltaGenerationComplete,
			Message: "not-json",
		},
	})
	require.Error(t, err)
	require.True(t, IsInvalidPayload(err))
}

func TestNewHandlerRequiresDependencies(t *testing.T) {
	completion := newCompletionServiceForTest(t, &completionStore{})
	status := workerservice.NewStorePreparingStatus(nil, completionStatusStore{})

	_, err := NewHandler(nil, status)
	require.EqualError(t, err, "completion service is required")

	_, err = NewHandler(completion, nil)
	require.EqualError(t, err, "preparing status service is required")
}

func TestHandlerHandleUpdatesProgressForIncompletePrepare(t *testing.T) {
	orgID := uuid.New()
	prepare := model.DeltaPrepare{
		ID:     uuid.New(),
		OrgID:  orgID,
		Kind:   domain.DeviceKind,
		Name:   "device-1",
		Status: model.DeltaPrepareWaiting,
	}
	generation := &model.DeltaGeneration{
		OrgID:           orgID,
		ImageRepository: "quay.io/example/os",
		SourceDigest:    "sha256:source",
		TargetDigest:    "sha256:target",
		Status:          model.DeltaGenerationSucceeded,
	}
	event, err := deltageneration.NewGenerationCompleteEvent(generation)
	require.NoError(t, err)
	store := &completionStore{progress: []deltapreparestore.PrepareProgress{{Prepare: prepare, Completed: 1, Total: 2}}}
	completion := newCompletionServiceForTest(t, store)
	resourceStore := &progressStatusStore{}
	handler, err := NewHandler(completion, workerservice.NewStorePreparingStatus(nil, resourceStore))
	require.NoError(t, err)

	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{OrgId: orgID, Event: *event})

	require.NoError(t, err)
	require.Equal(t, 1, resourceStore.calls)
	require.Equal(t, int64(1), resourceStore.completed)
	require.Equal(t, int64(2), resourceStore.total)
}

func TestHandlerHandleEmitsTerminalProgressForClaimedPrepare(t *testing.T) {
	orgID := uuid.New()
	prepare := model.DeltaPrepare{
		ID:     uuid.New(),
		OrgID:  orgID,
		Kind:   domain.DeviceKind,
		Name:   "device-1",
		Status: model.DeltaPrepareWaiting,
	}
	generation := &model.DeltaGeneration{
		OrgID:           orgID,
		ImageRepository: "quay.io/example/os",
		SourceDigest:    "sha256:source",
		TargetDigest:    "sha256:target",
		Status:          model.DeltaGenerationSucceeded,
	}
	event, err := deltageneration.NewGenerationCompleteEvent(generation)
	require.NoError(t, err)
	store := &completionStore{progress: []deltapreparestore.PrepareProgress{{Prepare: prepare, Completed: 1, Total: 2}}}
	completion, events := newCompletionServiceWithEventsForTest(t, store)
	handler, err := NewHandler(completion, workerservice.NewStorePreparingStatus(nil, &progressStatusStore{}))
	require.NoError(t, err)

	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{OrgId: orgID, Event: *event})

	require.NoError(t, err)
	require.Len(t, events.created, 1)
	require.Equal(t, domain.EventReasonDeltaGenerationProgress, events.created[0].Reason)
}

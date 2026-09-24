package preparecomplete

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
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

type completionEventServiceStub struct{}

func (completionEventServiceStub) CreateEvent(context.Context, uuid.UUID, *domain.Event) {}

func (completionEventServiceStub) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func newCompletionServiceForTest(t *testing.T, store deltapreparestore.Store) *deltaprepare.ServiceHandler {
	handler, err := deltaprepare.NewCompletionService(
		store,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		completionEventServiceStub{},
	)
	require.NoError(t, err)
	return handler
}

type completionStore struct {
	prepare *model.DeltaPrepare
}

func (s *completionStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error {
	return nil
}

func (s *completionStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	return deltapreparestore.PrepareAdmission{}, nil
}

func (s *completionStore) GetDeltaPrepareByID(context.Context, uuid.UUID, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return s.prepare, nil
}

func (s *completionStore) GetLatestDeltaPrepareForResource(context.Context, uuid.UUID, string, string, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return s.prepare, nil
}

func (s *completionStore) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, nil
}

func (s *completionStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (s *completionStore) DecrementPendingGenerationsForGeneration(context.Context, deltagenerationstore.GenerationKey, string) ([]deltapreparestore.PrepareProgress, error) {
	return nil, nil
}

var _ deltapreparestore.Store = (*completionStore)(nil)

func TestHandlerHandle(t *testing.T) {
	orgID := uuid.New()
	prepare := &model.DeltaPrepare{
		ID:                    uuid.New(),
		OrgID:                 orgID,
		Kind:                  domain.DeviceKind,
		Name:                  "device-1",
		SourceResourceVersion: 3,
		ResourceVersion:       2,
		Status:                model.DeltaPrepareComplete,
	}
	event, err := deltaprepare.NewPrepareCompletionEvent(prepare)
	require.NoError(t, err)

	store := &completionStore{prepare: prepare}
	completion := newCompletionServiceForTest(t, store)
	handler, err := NewHandler(completion)
	require.NoError(t, err)
	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{OrgId: orgID, Event: *event})
	require.NoError(t, err)
}

func TestHandlerHandleInvalidPayload(t *testing.T) {
	completion := newCompletionServiceForTest(t, &completionStore{})
	handler, err := NewHandler(completion)
	require.NoError(t, err)
	err = handler.Handle(context.Background(), worker_client.EventWithOrgId{
		OrgId: uuid.New(),
		Event: domain.Event{Reason: domain.EventReasonDeltaPrepareComplete, Message: "not-json"},
	})
	require.Error(t, err)
	require.True(t, IsInvalidPayload(err))
}

func TestNewHandlerRequiresCompletionService(t *testing.T) {
	_, err := NewHandler(nil)
	require.EqualError(t, err, "completion service is required")
}

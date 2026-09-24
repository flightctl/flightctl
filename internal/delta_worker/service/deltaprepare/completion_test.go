package deltaprepare

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
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

func newCompletionServiceForTest(t *testing.T, store deltapreparestore.Store) *ServiceHandler {
	handler, err := NewCompletionService(
		store,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		completionEventServiceStub{},
	)
	require.NoError(t, err)
	return handler
}

func TestResumeCompletedPrepareUsesProvidedPrepare(t *testing.T) {
	store := &recordingStore{}
	handler := newCompletionServiceForTest(t, store)

	prepare := &model.DeltaPrepare{
		ID:                    uuid.New(),
		Kind:                  domain.DeviceKind,
		Name:                  "device-1",
		SourceResourceVersion: 3,
		Status:                model.DeltaPrepareComplete,
		ResourceVersion:       2,
	}
	require.NoError(t, handler.ResumeCompletedPrepare(context.Background(), prepare))
	require.Zero(t, store.gets)
}

func TestNewCompletionServiceRequiresStore(t *testing.T) {
	_, err := NewCompletionService(
		nil,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		completionEventServiceStub{},
	)
	require.EqualError(t, err, "delta prepare store is required")
}

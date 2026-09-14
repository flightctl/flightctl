package deltapreparegeneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeGenerationService struct {
	generations []model.DeltaGeneration
}

func (f *fakeGenerationService) CreateDeltaGenerations(context.Context, []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return nil, nil
}
func (f *fakeGenerationService) GetDeltaGeneration(context.Context, deltastore.GenerationKey, ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return nil, nil
}
func (f *fakeGenerationService) ListDeltaGenerations(context.Context, []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	return f.generations, nil
}
func (f *fakeGenerationService) UpdateDeltaGeneration(context.Context, int64, *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	return nil, nil
}

type fakePrepareService struct {
	prepare *model.DeltaPrepare
}

func (f *fakePrepareService) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error {
	return nil
}
func (f *fakePrepareService) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltastore.PrepareAdmission, error) {
	return deltastore.PrepareAdmission{}, nil
}
func (f *fakePrepareService) GetDeltaPrepare(context.Context, deltastore.PrepareKey, ...deltastore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return f.prepare, nil
}
func (f *fakePrepareService) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, nil
}
func (f *fakePrepareService) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}
func (f *fakePrepareService) CountDeltaPrepareGenerations(context.Context, uuid.UUID) (int, int, error) {
	return 0, 0, nil
}
func (f *fakePrepareService) SetDeltaPreparingStatus(context.Context, uuid.UUID, string, string, int, int) error {
	return nil
}
func (f *fakePrepareService) ClearDeltaPreparingStatus(context.Context, uuid.UUID, string, string) error {
	return nil
}

type recordingEvents struct {
	events.Service
	created []*domain.Event
}

func (e *recordingEvents) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	e.created = append(e.created, event)
}

var _ deltageneration.Service = (*fakeGenerationService)(nil)
var _ deltaprepare.Service = (*fakePrepareService)(nil)

func TestServiceCreateDeltaPrepareGenerationsReportsExistingProgress(t *testing.T) {
	orgID := uuid.New()
	prepareID := uuid.New()
	key := deltastore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{PrepareID: prepareID, OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest}}}
	events := &recordingEvents{}
	h := NewServiceHandler(joins, &fakeGenerationService{generations: []model.DeltaGeneration{{OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest, Status: model.DeltaGenerationInProgress}}}, &fakePrepareService{prepare: &model.DeltaPrepare{ID: prepareID, OrgID: orgID, Kind: domain.DeviceKind, Name: "device-1", Status: model.DeltaPrepareWaiting}}, events)

	err := h.CreateDeltaPrepareGenerations(context.Background(), joins.joins)

	require.NoError(t, err)
	require.Len(t, events.created, 1)
	require.Equal(t, domain.EventReasonDeltaGenerationProgress, events.created[0].Reason)
}

func TestServiceListDeltaPrepareGenerationsDelegatesToStore(t *testing.T) {
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{PrepareID: uuid.New()}}}
	h := NewServiceHandler(joins, nil, nil, nil)

	got, err := h.ListDeltaPrepareGenerations(context.Background(), deltastore.DeltaPrepareGenerationListFilter{})

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, joins.joins[0].PrepareID, got[0].PrepareID)
}

type recordingJoinStore struct {
	joins []*model.DeltaPrepareGeneration
}

func (s *recordingJoinStore) CreateDeltaPrepareGenerations(_ context.Context, joins []*model.DeltaPrepareGeneration) ([]*model.DeltaPrepareGeneration, error) {
	return joins, nil
}
func (s *recordingJoinStore) ListDeltaPrepareGenerations(context.Context, deltastore.DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error) {
	result := make([]model.DeltaPrepareGeneration, len(s.joins))
	for i, join := range s.joins {
		result[i] = *join
	}
	return result, nil
}

var _ deltastore.DeltaPrepareGenerationStore = (*recordingJoinStore)(nil)

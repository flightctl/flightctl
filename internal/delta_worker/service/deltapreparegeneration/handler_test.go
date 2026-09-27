package deltapreparegeneration

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeGenerationService struct {
	generations []model.DeltaGeneration
	err         error
}

func (f *fakeGenerationService) CreateDeltaGenerations(context.Context, []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return nil, nil
}
func (f *fakeGenerationService) GetDeltaGeneration(context.Context, deltagenerationstore.GenerationKey, ...deltagenerationstore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return nil, nil
}
func (f *fakeGenerationService) ListDeltaGenerations(context.Context, []deltagenerationstore.GenerationKey) ([]model.DeltaGeneration, error) {
	return f.generations, f.err
}
func (f *fakeGenerationService) UpdateDeltaGeneration(context.Context, int64, *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	return nil, nil
}

type fakePrepareService struct {
	prepare     *model.DeltaPrepare
	statusCalls int
}

func (f *fakePrepareService) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error {
	return nil
}
func (f *fakePrepareService) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	return deltapreparestore.PrepareAdmission{}, nil
}
func (f *fakePrepareService) GetDeltaPrepareByID(context.Context, uuid.UUID, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return f.prepare, nil
}
func (f *fakePrepareService) GetLatestDeltaPrepareForResource(context.Context, uuid.UUID, string, string, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return f.prepare, nil
}
func (f *fakePrepareService) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	if f.prepare == nil {
		return nil, nil
	}
	return []model.DeltaPrepare{*f.prepare}, nil
}
func (f *fakePrepareService) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}
func (f *fakePrepareService) DecrementPendingGenerationsForGeneration(context.Context, deltagenerationstore.GenerationKey, string) ([]deltapreparestore.PrepareProgress, error) {
	return nil, nil
}
func (f *fakePrepareService) SetDeltaPreparingStatus(_ context.Context, _ *model.DeltaPrepare, completed, total int) error {
	f.statusCalls++
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
	key := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
	joins := &recordingJoinStore{
		joins: []*model.DeltaPrepareGeneration{{PrepareID: prepareID, OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest}},
		updatedPrepares: map[uuid.UUID]model.DeltaPrepare{prepareID: {
			ID: prepareID, OrgID: orgID, Kind: domain.DeviceKind, Name: "device-1", Status: model.DeltaPrepareWaiting,
		}},
	}
	events := &recordingEvents{}
	h := NewServiceHandler(joins, &fakeGenerationService{generations: []model.DeltaGeneration{{OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest, Status: model.DeltaGenerationInProgress}}}, events)

	_, err := h.CreateDeltaPrepareGenerations(context.Background(), joins.joins)

	require.NoError(t, err)
	require.Len(t, events.created, 1)
	require.Equal(t, domain.EventReasonDeltaGenerationProgress, events.created[0].Reason)
}

func TestProgressServiceSkipsTerminalProgress(t *testing.T) {
	orgID := uuid.New()
	prepareID := uuid.New()
	key := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{PrepareID: prepareID, OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest}}}
	prepares := &fakePrepareService{
		prepare: &model.DeltaPrepare{ID: prepareID, OrgID: orgID, Kind: domain.DeviceKind, Name: "device-1", Status: model.DeltaPrepareWaiting},
	}
	events := &recordingEvents{}
	h := NewProgressHandler(joins, prepares, events)
	generation := &model.DeltaGeneration{
		OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		Status: model.DeltaGenerationSucceeded,
	}

	err := h.EmitForGeneration(context.Background(), generation)

	require.NoError(t, err)
	require.Empty(t, events.created)
	require.Zero(t, prepares.statusCalls)
}

func TestServiceCreateDeltaPrepareGenerationsPropagatesGenerationListError(t *testing.T) {
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{PrepareID: uuid.New()}}}
	events := &recordingEvents{}
	wantErr := errors.New("generation store unavailable")
	h := NewServiceHandler(joins, &fakeGenerationService{err: wantErr}, events)

	_, err := h.CreateDeltaPrepareGenerations(context.Background(), joins.joins)

	require.ErrorIs(t, err, wantErr)
	require.Empty(t, events.created)
}

func TestServiceCreateDeltaPrepareGenerationsSkipsMissingPrepare(t *testing.T) {
	orgID := uuid.New()
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{
		PrepareID: uuid.New(), OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target",
	}}}
	events := &recordingEvents{}
	h := NewServiceHandler(
		joins,
		&fakeGenerationService{generations: []model.DeltaGeneration{{
			OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target", Status: model.DeltaGenerationInProgress,
		}}},
		events,
	)

	_, err := h.CreateDeltaPrepareGenerations(context.Background(), joins.joins)

	require.NoError(t, err)
	require.Empty(t, events.created)
}

func TestServiceListDeltaPrepareGenerationsDelegatesToStore(t *testing.T) {
	joins := &recordingJoinStore{joins: []*model.DeltaPrepareGeneration{{PrepareID: uuid.New()}}}
	h := NewServiceHandler(joins, nil, nil)

	got, err := h.ListDeltaPrepareGenerations(context.Background(), deltapreparegenerationstore.ListFilter{})

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, joins.joins[0].PrepareID, got[0].PrepareID)
}

type recordingJoinStore struct {
	joins           []*model.DeltaPrepareGeneration
	updatedPrepares map[uuid.UUID]model.DeltaPrepare
}

func (s *recordingJoinStore) CreateDeltaPrepareGenerations(_ context.Context, joins []*model.DeltaPrepareGeneration) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error) {
	return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{InsertedJoins: joins, UpdatedPrepares: s.updatedPrepares}, nil
}
func (s *recordingJoinStore) ListDeltaPrepareGenerations(context.Context, deltapreparegenerationstore.ListFilter) ([]model.DeltaPrepareGeneration, error) {
	result := make([]model.DeltaPrepareGeneration, len(s.joins))
	for i, join := range s.joins {
		result[i] = *join
	}
	return result, nil
}

var _ deltapreparegenerationstore.Store = (*recordingJoinStore)(nil)

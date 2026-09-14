package deltageneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeGenerationStore struct {
	generation *model.DeltaGeneration
}

func (f *fakeGenerationStore) InsertDeltaGenerations(_ context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	if len(generations) > 0 {
		f.generation = cloneGeneration(generations[0])
		if f.generation.Status == "" {
			f.generation.Status = model.DeltaGenerationPending
		}
	}
	if f.generation == nil {
		return nil, nil
	}
	return []model.DeltaGeneration{*cloneGeneration(f.generation)}, nil
}

func (f *fakeGenerationStore) GetDeltaGeneration(_ context.Context, _ deltastore.GenerationKey, _ ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return cloneGeneration(f.generation), nil
}

func (f *fakeGenerationStore) ListDeltaGenerations(_ context.Context, _ []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	if f.generation == nil {
		return nil, nil
	}
	return []model.DeltaGeneration{*cloneGeneration(f.generation)}, nil
}

func (f *fakeGenerationStore) UpdateDeltaGeneration(_ context.Context, expected int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	if f.generation.ResourceVersion != expected {
		return nil, flterrors.ErrNoRowsUpdated
	}
	f.generation = cloneGeneration(generation)
	f.generation.ResourceVersion++
	return cloneGeneration(f.generation), nil
}

type fakePrepareStore struct {
	prepares []model.DeltaPrepare
}

func (f *fakePrepareStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error { return nil }
func (f *fakePrepareStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltastore.PrepareAdmission, error) {
	return deltastore.PrepareAdmission{}, nil
}
func (f *fakePrepareStore) GetDeltaPrepare(_ context.Context, key deltastore.PrepareKey, _ ...deltastore.PrepareGetOption) (*model.DeltaPrepare, error) {
	for i := range f.prepares {
		if key.ID != uuid.Nil && f.prepares[i].ID == key.ID {
			return &f.prepares[i], nil
		}
	}
	return nil, nil
}
func (f *fakePrepareStore) ListDeltaPrepares(_ context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	set := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	var result []model.DeltaPrepare
	for _, prepare := range f.prepares {
		if _, ok := set[prepare.ID]; ok {
			result = append(result, prepare)
		}
	}
	return result, nil
}
func (f *fakePrepareStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}
func (f *fakePrepareStore) CountDeltaPrepareGenerations(context.Context, uuid.UUID) (int, int, error) {
	return 1, 2, nil
}

type fakeJoinStore struct {
	joins []model.DeltaPrepareGeneration
}

func (f *fakeJoinStore) CreateDeltaPrepareGenerations(_ context.Context, joins []*model.DeltaPrepareGeneration) ([]*model.DeltaPrepareGeneration, error) {
	return joins, nil
}
func (f *fakeJoinStore) ListDeltaPrepareGenerations(_ context.Context, filter deltastore.DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error) {
	var result []model.DeltaPrepareGeneration
	for _, join := range f.joins {
		if filter.GenerationKey != nil && (join.OrgID != filter.GenerationKey.OrgID || join.ImageRepository != filter.GenerationKey.ImageRepository || join.SourceDigest != filter.GenerationKey.SourceDigest || join.TargetDigest != filter.GenerationKey.TargetDigest) {
			continue
		}
		result = append(result, join)
	}
	return result, nil
}

type recordingEvents struct {
	events.Service
	created []*domain.Event
}

func (e *recordingEvents) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	e.created = append(e.created, event)
}

type recordingStatus struct {
	calls int
}

func (p *recordingStatus) Set(context.Context, uuid.UUID, string, string, int, int) error {
	p.calls++
	return nil
}

func (p *recordingStatus) Clear(context.Context, uuid.UUID, string, string) error { return nil }

func TestServiceUpdateDeltaGenerationEmitsProgressToWaitingPrepares(t *testing.T) {
	orgID := uuid.New()
	prepareID := uuid.New()
	key := deltastore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
	store := &fakeGenerationStore{generation: &model.DeltaGeneration{
		OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		Status: model.DeltaGenerationInProgress, ResourceVersion: 4,
	}}
	prepares := &fakePrepareStore{prepares: []model.DeltaPrepare{{ID: prepareID, OrgID: orgID, Kind: domain.DeviceKind, Name: "device-1", Status: model.DeltaPrepareWaiting}}}
	joins := &fakeJoinStore{joins: []model.DeltaPrepareGeneration{{PrepareID: prepareID, OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest}}}
	events := &recordingEvents{}
	status := &recordingStatus{}
	h := NewServiceHandler(store, prepares, joins, events, status, nil)

	generation := cloneGeneration(store.generation)
	generation.Status = model.DeltaGenerationSucceeded
	_, err := h.UpdateDeltaGeneration(context.Background(), 4, generation)

	require.NoError(t, err)
	require.Len(t, events.created, 1)
	require.Equal(t, domain.EventReasonDeltaGenerationProgress, events.created[0].Reason)
	require.Equal(t, 1, status.calls)
}

func cloneGeneration(generation *model.DeltaGeneration) *model.DeltaGeneration {
	if generation == nil {
		return nil
	}
	copy := *generation
	return &copy
}

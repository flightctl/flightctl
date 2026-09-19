package deltageneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeGenerationStore struct {
	generation        *model.DeltaGeneration
	persistedOnInsert *model.DeltaGeneration
}

func (f *fakeGenerationStore) InsertDeltaGenerations(_ context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	if len(generations) > 0 {
		if f.persistedOnInsert != nil {
			f.generation = cloneGeneration(f.persistedOnInsert)
		} else {
			f.generation = cloneGeneration(generations[0])
		}
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

type progressCall struct {
	key    deltastore.GenerationKey
	status domain.DeltaGenerationProgressDetailsGenerationStatus
	phase  *domain.DeltaGenerationPhase
}

type progressSpy struct {
	calls []progressCall
}

func (s *progressSpy) EmitForGeneration(_ context.Context, generation *model.DeltaGeneration, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) error {
	s.calls = append(s.calls, progressCall{key: generationKeyOf(generation), status: status, phase: phase})
	return nil
}

func TestServiceUpdateDeltaGenerationNotifiesProgressService(t *testing.T) {
	tests := []struct {
		name             string
		expectedRV       int64
		generationStatus string
		wantErr          bool
		wantCalls        int
	}{
		{name: "When a nonterminal generation is updated it should notify progress", expectedRV: 4, generationStatus: model.DeltaGenerationInProgress, wantCalls: 1},
		{name: "When the resource version is stale it should not notify progress", expectedRV: 3, generationStatus: model.DeltaGenerationInProgress, wantErr: true},
		{name: "When a terminal generation is updated it should not notify progress", expectedRV: 4, generationStatus: model.DeltaGenerationSucceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orgID := uuid.New()
			key := deltastore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
			store := &fakeGenerationStore{generation: &model.DeltaGeneration{
				OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
				Status: model.DeltaGenerationInProgress, ResourceVersion: 4,
			}}
			progress := &progressSpy{}
			h := NewServiceHandler(store, progress.EmitForGeneration, nil)

			generation := cloneGeneration(store.generation)
			generation.Status = tt.generationStatus
			_, err := h.UpdateDeltaGeneration(context.Background(), tt.expectedRV, generation)

			if tt.wantErr {
				require.ErrorIs(t, err, flterrors.ErrNoRowsUpdated)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, progress.calls, tt.wantCalls)
		})
	}
}

func TestServiceCreateDeltaGenerationsSkipsTerminalProgress(t *testing.T) {
	orgID := uuid.New()
	key := deltastore.GenerationKey{OrgID: orgID, ImageRepository: "quay.io/example/os", SourceDigest: "sha256:source", TargetDigest: "sha256:target"}
	phase := string(domain.DeltaGenerationPhasePush)
	store := &fakeGenerationStore{persistedOnInsert: &model.DeltaGeneration{
		OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		Status: model.DeltaGenerationSucceeded, Phase: &phase,
	}}
	progress := &progressSpy{}
	h := NewServiceHandler(store, progress.EmitForGeneration, nil)

	_, err := h.CreateDeltaGenerations(context.Background(), []*model.DeltaGeneration{{
		OrgID: orgID, ImageRepository: key.ImageRepository, SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		Status: model.DeltaGenerationRejected,
	}})

	require.NoError(t, err)
	require.Empty(t, progress.calls)
}

func cloneGeneration(generation *model.DeltaGeneration) *model.DeltaGeneration {
	if generation == nil {
		return nil
	}
	copy := *generation
	return &copy
}

package deltageneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/flterrors"
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

func TestServiceCreateDeltaGenerationsReturnsPersistedGenerations(t *testing.T) {
	persisted := &model.DeltaGeneration{Status: model.DeltaGenerationSucceeded}
	store := &fakeGenerationStore{persistedOnInsert: persisted}
	h := NewServiceHandler(store, nil)

	created, err := h.CreateDeltaGenerations(context.Background(), []*model.DeltaGeneration{{Status: model.DeltaGenerationPending}})

	require.NoError(t, err)
	require.Len(t, created, 1)
	require.Equal(t, model.DeltaGenerationSucceeded, created[0].Status)
}

func TestServiceUpdateDeltaGenerationDelegatesCAS(t *testing.T) {
	store := &fakeGenerationStore{generation: &model.DeltaGeneration{ResourceVersion: 4}}
	h := NewServiceHandler(store, nil)
	generation := cloneGeneration(store.generation)
	generation.Status = model.DeltaGenerationSucceeded

	updated, err := h.UpdateDeltaGeneration(context.Background(), 4, generation)

	require.NoError(t, err)
	require.Equal(t, model.DeltaGenerationSucceeded, updated.Status)
	require.Equal(t, int64(5), updated.ResourceVersion)

	_, err = h.UpdateDeltaGeneration(context.Background(), 4, generation)
	require.ErrorIs(t, err, flterrors.ErrNoRowsUpdated)
}

func cloneGeneration(generation *model.DeltaGeneration) *model.DeltaGeneration {
	if generation == nil {
		return nil
	}
	copy := *generation
	return &copy
}

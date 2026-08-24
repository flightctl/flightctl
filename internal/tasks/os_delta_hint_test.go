package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type stubGenerationLookup struct {
	mu    sync.Mutex
	calls int
	gen   *model.DeltaGeneration
	err   error
}

func (s *stubGenerationLookup) GetGeneration(_ context.Context, _ delta.GenerationKey) (*model.DeltaGeneration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.gen, nil
}

func (s *stubGenerationLookup) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func testGenerationKey() delta.GenerationKey {
	return delta.GenerationKey{
		OrgID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ImageRepository: "quay.io/acme/os",
		SourceDigest:    "sha256:aaa",
		TargetDigest:    "sha256:bbb",
	}
}

func TestLookupCachedGeneration(t *testing.T) {
	ctx := context.Background()
	key := testGenerationKey()
	row := &model.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          model.DeltaGenerationSucceeded,
		DeltaRef:        lo.ToPtr("quay.io/acme/os@sha256:delta"),
		SizeBytes:       lo.ToPtr(int64(47185920)),
	}

	t.Run("When the cache is empty it should load from the store and return the row", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{gen: row}

		got, err := lookupCachedGeneration(ctx, kv, store, key)
		require.NoError(t, err)
		require.Equal(t, row, got)
		require.Equal(t, 1, store.callCount())
		require.True(t, kv.has(generationMemoKey(key)))
	})

	t.Run("When the cache is populated it should not query the store again", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{gen: row}

		_, err := lookupCachedGeneration(ctx, kv, store, key)
		require.NoError(t, err)
		got, err := lookupCachedGeneration(ctx, kv, store, key)
		require.NoError(t, err)
		require.Equal(t, row.Status, got.Status)
		require.Equal(t, row.DeltaRef, got.DeltaRef)
		require.Equal(t, row.SizeBytes, got.SizeBytes)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store has no row it should cache the miss", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: flterrors.ErrResourceNotFound}

		got, err := lookupCachedGeneration(ctx, kv, store, key)
		require.NoError(t, err)
		require.Nil(t, got)
		got, err = lookupCachedGeneration(ctx, kv, store, key)
		require.NoError(t, err)
		require.Nil(t, got)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store fails it should not cache the error", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: errors.New("db down")}

		_, err := lookupCachedGeneration(ctx, kv, store, key)
		require.Error(t, err)
		_, err = lookupCachedGeneration(ctx, kv, store, key)
		require.Error(t, err)
		require.Equal(t, 2, store.callCount())
	})
}

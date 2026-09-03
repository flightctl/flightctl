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

func (s *stubGenerationLookup) GetGeneration(_ context.Context, _ delta.GenerationKey, _ ...delta.GenerationGetOption) (*model.DeltaGeneration, error) {
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

func TestImageRepositoryFromRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{name: "When the ref is a tag it should strip the tag", ref: "quay.io/acme/os:latest", want: "quay.io/acme/os"},
		{name: "When the ref is digested it should strip the digest", ref: "quay.io/acme/os@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", want: "quay.io/acme/os"},
		{name: "When the ref is empty it should return an error", ref: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ImageRepositoryFromRef(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHintFromGeneration(t *testing.T) {
	deltaRef := "quay.io/acme/os@sha256:delta"
	size := int64(47185920)
	full := int64(1 << 30)

	t.Run("When generation succeeded it should hint deltaRef and IEC size_bytes", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{
			Status:    model.DeltaGenerationSucceeded,
			DeltaRef:  &deltaRef,
			SizeBytes: &size,
		}, nil)
		require.Equal(t, &deltaRef, img)
		require.Equal(t, lo.ToPtr("45 MiB"), sz)
	})

	t.Run("When generation is rejected it should not hint and should use size_bytes", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{
			Status:    model.DeltaGenerationRejected,
			SizeBytes: &size,
		}, nil)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("45 MiB"), sz)
	})

	t.Run("When generation is missing it should not hint and should use fallback size", func(t *testing.T) {
		img, sz := hintFromGeneration(nil, &full)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("1 GiB"), sz)
	})

	t.Run("When generation failed without size_bytes it should use fallback size", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{Status: model.DeltaGenerationFailed}, &full)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("1 GiB"), sz)
	})
}

func TestFormatIECBytes(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{name: "When the size is zero it should report 0 KiB", n: 0, want: "0 KiB"},
		{name: "When the size is below one KiB it should report 1 KiB", n: 1, want: "1 KiB"},
		{name: "When the size is exactly 1024 it should report 1 KiB", n: 1024, want: "1 KiB"},
		{name: "When the size is 45 MiB it should report 45 MiB", n: 47185920, want: "45 MiB"},
		{name: "When the size is 1 GiB it should report 1 GiB", n: 1 << 30, want: "1 GiB"},
		{name: "When the size is 1 TiB it should report 1 TiB", n: 1 << 40, want: "1 TiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FormatIECBytes(tt.n))
		})
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

		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Equal(t, row, got)
		require.Equal(t, 1, store.callCount())
		require.True(t, kv.has(generationMemoKey(key, "")))
	})

	t.Run("When the cache is populated it should not query the store again", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{gen: row}

		_, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Equal(t, row.Status, got.Status)
		require.Equal(t, row.DeltaRef, got.DeltaRef)
		require.Equal(t, row.SizeBytes, got.SizeBytes)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store has no row it should cache the miss", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: flterrors.ErrResourceNotFound}

		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Nil(t, got)
		got, err = lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Nil(t, got)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store fails it should not cache the error", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: errors.New("db down")}

		_, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.Error(t, err)
		_, err = lookupCachedGeneration(ctx, kv, store, key, "")
		require.Error(t, err)
		require.Equal(t, 2, store.callCount())
	})
}

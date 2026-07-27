package canary

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/stretchr/testify/require"
)

type fakeCanaryStore struct {
	canaries map[string]*model.EncryptionCanary
	err      error
}

func newFakeCanaryStore() *fakeCanaryStore {
	return &fakeCanaryStore{canaries: map[string]*model.EncryptionCanary{}}
}

func storeKey(strategy, keyID string) string { return strategy + "/" + keyID }

func (f *fakeCanaryStore) InitialMigration(_ context.Context) error { return f.err }

func (f *fakeCanaryStore) Get(_ context.Context, strategy, keyID string) (*model.EncryptionCanary, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.canaries[storeKey(strategy, keyID)]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return c, nil
}

func (f *fakeCanaryStore) Create(_ context.Context, canary *model.EncryptionCanary) error {
	if f.err != nil {
		return f.err
	}
	f.canaries[storeKey(canary.Strategy, canary.KeyID)] = canary
	return nil
}

func (f *fakeCanaryStore) CreateOrUpdate(_ context.Context, canary *model.EncryptionCanary) error {
	if f.err != nil {
		return f.err
	}
	f.canaries[storeKey(canary.Strategy, canary.KeyID)] = canary
	return nil
}

func (f *fakeCanaryStore) Delete(_ context.Context, strategy, keyID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	k := storeKey(strategy, keyID)
	if _, ok := f.canaries[k]; !ok {
		return false, nil
	}
	delete(f.canaries, k)
	return true, nil
}

func (f *fakeCanaryStore) List(_ context.Context) ([]model.EncryptionCanary, error) {
	if f.err != nil {
		return nil, f.err
	}
	result := make([]model.EncryptionCanary, 0, len(f.canaries))
	for _, c := range f.canaries {
		result = append(result, *c)
	}
	return result, nil
}

func TestGet(t *testing.T) {
	t.Run("When canary exists it should return it with StatusOK", func(t *testing.T) {
		fake := newFakeCanaryStore()
		_ = fake.Create(context.Background(), &model.EncryptionCanary{
			Strategy:       "v1",
			KeyID:          "key1",
			EncryptedValue: []byte("enc-value"),
		})
		h := NewServiceHandler(fake)

		canary, status := h.Get(context.Background(), "v1", "key1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, canary)
		require.Equal(t, "v1", canary.Strategy)
		require.Equal(t, "key1", canary.KeyID)
	})

	t.Run("When canary does not exist it should return a not-found status", func(t *testing.T) {
		fake := newFakeCanaryStore()
		h := NewServiceHandler(fake)

		_, status := h.Get(context.Background(), "v1", "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When store returns an error it should return an internal-error status", func(t *testing.T) {
		fake := newFakeCanaryStore()
		fake.err = errors.New("db down")
		h := NewServiceHandler(fake)

		_, status := h.Get(context.Background(), "v1", "key1")
		require.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})
}

func TestSave(t *testing.T) {
	t.Run("When saving a new canary it should return StatusOK", func(t *testing.T) {
		fake := newFakeCanaryStore()
		h := NewServiceHandler(fake)

		status := h.Save(context.Background(), &encryption.Canary{
			Strategy:       "v1",
			KeyID:          "key1",
			EncryptedValue: []byte("enc-value"),
			CreatedAt:      time.Now(),
		})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, fake.canaries, 1)
	})

	t.Run("When store fails it should return an internal-error status", func(t *testing.T) {
		fake := newFakeCanaryStore()
		fake.err = errors.New("db down")
		h := NewServiceHandler(fake)

		status := h.Save(context.Background(), &encryption.Canary{
			Strategy: "v1",
			KeyID:    "key1",
		})
		require.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})
}

func TestGetAll(t *testing.T) {
	t.Run("When canaries exist it should return all with StatusOK", func(t *testing.T) {
		fake := newFakeCanaryStore()
		_ = fake.Create(context.Background(), &model.EncryptionCanary{
			Strategy: "v1", KeyID: "key1", EncryptedValue: []byte("a"),
		})
		_ = fake.Create(context.Background(), &model.EncryptionCanary{
			Strategy: "v1", KeyID: "key2", EncryptedValue: []byte("b"),
		})
		h := NewServiceHandler(fake)

		canaries, status := h.GetAll(context.Background())
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, canaries, 2)
	})

	t.Run("When store is empty it should return empty slice with StatusOK", func(t *testing.T) {
		fake := newFakeCanaryStore()
		h := NewServiceHandler(fake)

		canaries, status := h.GetAll(context.Background())
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Empty(t, canaries)
	})

	t.Run("When store fails it should return an internal-error status", func(t *testing.T) {
		fake := newFakeCanaryStore()
		fake.err = errors.New("db down")
		h := NewServiceHandler(fake)

		_, status := h.GetAll(context.Background())
		require.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})
}

func TestPrepareForRetirement(t *testing.T) {
	t.Run("When canary exists it should delete it and return StatusOK", func(t *testing.T) {
		fake := newFakeCanaryStore()
		_ = fake.Create(context.Background(), &model.EncryptionCanary{
			Strategy: "v1", KeyID: "key1", EncryptedValue: []byte("a"),
		})
		h := NewServiceHandler(fake)

		status := h.PrepareForRetirement(context.Background(), "v1", "key1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Empty(t, fake.canaries)
	})

	t.Run("When canary does not exist it should return StatusOK (idempotent)", func(t *testing.T) {
		fake := newFakeCanaryStore()
		h := NewServiceHandler(fake)

		status := h.PrepareForRetirement(context.Background(), "v1", "missing")
		require.Equal(t, int32(http.StatusOK), status.Code)
	})

	t.Run("When store fails it should return an internal-error status", func(t *testing.T) {
		fake := newFakeCanaryStore()
		fake.err = errors.New("db down")
		h := NewServiceHandler(fake)

		status := h.PrepareForRetirement(context.Background(), "v1", "key1")
		require.Equal(t, int32(http.StatusInternalServerError), status.Code)
	})
}

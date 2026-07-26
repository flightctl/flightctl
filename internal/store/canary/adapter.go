package canary

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
)

type encryptionStoreAdapter struct {
	store Store
}

var _ encryption.CanaryStore = (*encryptionStoreAdapter)(nil)

func AsEncryptionStore(s Store) encryption.CanaryStore {
	return &encryptionStoreAdapter{store: s}
}

func (a *encryptionStoreAdapter) Get(ctx context.Context, strategy, keyID string) (*encryption.Canary, error) {
	row, err := a.store.Get(ctx, strategy, keyID)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return row.ToEncryptionCanary(), nil
}

func (a *encryptionStoreAdapter) Save(ctx context.Context, canary *encryption.Canary) error {
	return a.store.CreateOrUpdate(ctx, model.EncryptionCanaryFrom(canary))
}

func (a *encryptionStoreAdapter) GetAll(ctx context.Context) ([]encryption.Canary, error) {
	rows, err := a.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]encryption.Canary, len(rows))
	for i := range rows {
		result[i] = *rows[i].ToEncryptionCanary()
	}
	return result, nil
}

package canary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

type encryptionStoreAdapter struct {
	svc Service
}

var _ encryption.CanaryStore = (*encryptionStoreAdapter)(nil)

// AsEncryptionStore wraps a canary Service as an encryption.CanaryStore,
// converting domain.Status returns to errors.
func AsEncryptionStore(svc Service) encryption.CanaryStore {
	return &encryptionStoreAdapter{svc: svc}
}

func (a *encryptionStoreAdapter) Get(ctx context.Context, strategy, keyID string) (*encryption.Canary, error) {
	canary, status := a.svc.Get(ctx, strategy, keyID)
	if status.Code == http.StatusNotFound {
		return nil, nil
	}
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("%s", status.Message)
	}
	return canary, nil
}

func (a *encryptionStoreAdapter) Save(ctx context.Context, canary *encryption.Canary) error {
	status := a.svc.Save(ctx, canary)
	if status.Code != http.StatusOK {
		return fmt.Errorf("%s", status.Message)
	}
	return nil
}

func (a *encryptionStoreAdapter) GetAll(ctx context.Context) ([]encryption.Canary, error) {
	canaries, status := a.svc.GetAll(ctx)
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("%s", status.Message)
	}
	return canaries, nil
}

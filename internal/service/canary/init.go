package canary

import (
	"context"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// InitEncryption wires the canary store, service, and adapter, then
// registers everything with the global encryption manager. No-op if
// encryption is not initialized.
func InitEncryption(ctx context.Context, db *gorm.DB, log logrus.FieldLogger) error {
	store := canarystore.NewCanaryStore(db, log.WithField("pkg", "canary-store"))
	svc := WrapWithTracing(NewServiceHandler(store))
	return encryption.InitCanaryStore(ctx, AsEncryptionStore(svc))
}

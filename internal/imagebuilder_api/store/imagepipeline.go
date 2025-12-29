package store

import (
	"context"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ImagePipelineStore provides transaction support for atomic operations
type ImagePipelineStore interface {
	// Transaction executes fn within a database transaction, passing the transaction via context
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// imagePipelineStore is the concrete implementation
type imagePipelineStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// NewImagePipelineStore creates a new ImagePipelineStore
func NewImagePipelineStore(db *gorm.DB, log logrus.FieldLogger) ImagePipelineStore {
	return &imagePipelineStore{
		db:  db,
		log: log,
	}
}

// Transaction executes fn within a database transaction, passing the transaction via context
func (s *imagePipelineStore) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := WithTx(ctx, tx)
		return fn(txCtx)
	})
}

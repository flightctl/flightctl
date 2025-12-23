package store

import (
	"context"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// txKey is the context key for storing database transactions
type txKey struct{}

// WithTx returns a new context with the given transaction attached
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext retrieves a transaction from context, or nil if none exists
func TxFromContext(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx
	}
	return nil
}

// getDB returns the transaction from context if present, otherwise the provided db
func getDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx := TxFromContext(ctx); tx != nil {
		return tx
	}
	return db
}

// Store is the imagebuilder-specific store interface
type Store interface {
	ImageBuild() ImageBuildStore
	ImageExport() ImageExportStore
	ImagePipeline() ImagePipelineStore
	RunMigrations(ctx context.Context) error
	Ping() error
	Close() error
}

// storeImpl is the concrete implementation of the imagebuilder Store interface
type storeImpl struct {
	imageBuild    ImageBuildStore
	imageExport   ImageExportStore
	imagePipeline ImagePipelineStore
	db            *gorm.DB
	log           logrus.FieldLogger
}

// NewStore creates a new imagebuilder store
func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &storeImpl{
		imageBuild:    NewImageBuildStore(db, log),
		imageExport:   NewImageExportStore(db, log),
		imagePipeline: NewImagePipelineStore(db, log),
		db:            db,
		log:           log,
	}
}

// ImageBuild returns the ImageBuild store
func (s *storeImpl) ImageBuild() ImageBuildStore {
	return s.imageBuild
}

// ImageExport returns the ImageExport store
func (s *storeImpl) ImageExport() ImageExportStore {
	return s.imageExport
}

// ImagePipeline returns the ImagePipeline store
func (s *storeImpl) ImagePipeline() ImagePipelineStore {
	return s.imagePipeline
}

// RunMigrations runs the imagebuilder-specific migrations
func (s *storeImpl) RunMigrations(ctx context.Context) error {
	if err := s.imageBuild.InitialMigration(ctx); err != nil {
		return err
	}
	return s.imageExport.InitialMigration(ctx)
}

// Ping checks database connectivity
func (s *storeImpl) Ping() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// Close closes the database connection
func (s *storeImpl) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

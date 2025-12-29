package store

import (
	"context"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Store is the imagebuilder-specific store interface
type Store interface {
	ImageBuild() ImageBuildStore
	ImageExport() ImageExportStore
	RunMigrations(ctx context.Context) error
	Ping() error
	Close() error
}

// storeImpl is the concrete implementation of the imagebuilder Store interface
type storeImpl struct {
	imageBuild  ImageBuildStore
	imageExport ImageExportStore
	db          *gorm.DB
	log         logrus.FieldLogger
}

// NewStore creates a new imagebuilder store
func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &storeImpl{
		imageBuild:  NewImageBuildStore(db, log),
		imageExport: NewImageExportStore(db, log),
		db:          db,
		log:         log,
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

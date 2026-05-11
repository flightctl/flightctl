// Copyright (c) Flight Control Authors. Licensed under Apache-2.0.

package store

import (
	"context"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type SyncState interface {
	InitialMigration(ctx context.Context) error
}

type SyncStateStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

var _ SyncState = (*SyncStateStore)(nil)

func NewSyncState(db *gorm.DB, log logrus.FieldLogger) SyncState {
	return &SyncStateStore{dbHandler: db, log: log}
}

func (s *SyncStateStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *SyncStateStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.SyncState{})
}

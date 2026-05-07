// Copyright (c) Flight Control Authors. Licensed under Apache-2.0.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type SyncState interface {
	InitialMigration(ctx context.Context) error
	Get(ctx context.Context, orgID uuid.UUID, resourceKey string) (*model.SyncState, error)
	Set(ctx context.Context, orgID uuid.UUID, state *model.SyncState) error
	SetLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKey string, t time.Time) error
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

func (s *SyncStateStore) Get(ctx context.Context, orgID uuid.UUID, resourceKey string) (*model.SyncState, error) {
	var state model.SyncState
	result := s.getDB(ctx).Where("org_id = ? AND resource_key = ?", orgID, resourceKey).Take(&state)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrorFromGormError(result.Error)
	}
	return &state, nil
}

func (s *SyncStateStore) Set(ctx context.Context, orgID uuid.UUID, state *model.SyncState) error {
	state.OrgID = orgID
	result := s.getDB(ctx).Save(state)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

func (s *SyncStateStore) SetLastCheckedAt(ctx context.Context, orgID uuid.UUID, resourceKey string, t time.Time) error {
	result := s.getDB(ctx).Model(&model.SyncState{}).
		Where("org_id = ? AND resource_key = ?", orgID, resourceKey).
		Update("last_checked_at", t)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

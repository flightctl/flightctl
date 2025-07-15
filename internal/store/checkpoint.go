package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Checkpoint interface {
	InitialMigration(ctx context.Context) error
	Set(ctx context.Context, consumer string, key string, value []byte) error
	Get(ctx context.Context, consumer string, key string) ([]byte, error)
	GetDatabaseTime(ctx context.Context) (time.Time, error)
}

type CheckpointStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// Make sure we conform to Checkpoint interface
var _ Checkpoint = (*CheckpointStore)(nil)

func NewCheckpoint(db *gorm.DB, log logrus.FieldLogger) Checkpoint {
	return &CheckpointStore{dbHandler: db, log: log}
}

func (s *CheckpointStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *CheckpointStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	return db.AutoMigrate(&model.Checkpoint{})
}

func (s *CheckpointStore) Set(ctx context.Context, consumer string, key string, value []byte) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(value)
	if err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	checkpoint := model.Checkpoint{
		Consumer: consumer,
		Key:      key,
		Value:    buf.Bytes(),
	}
	return s.getDB(ctx).Save(&checkpoint).Error
}

func (s *CheckpointStore) Get(ctx context.Context, consumer string, key string) ([]byte, error) {
	var checkpoint model.Checkpoint
	result := s.getDB(ctx).First(&checkpoint, "consumer = ? AND key = ?", consumer, key)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, result.Error
	}

	zr, err := gzip.NewReader(bytes.NewReader(checkpoint.Value))
	if err != nil {
		return nil, err
	}
	decompressed, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if err := zr.Close(); err != nil {
		return nil, err
	}

	return decompressed, nil
}

func (s *CheckpointStore) GetDatabaseTime(ctx context.Context) (time.Time, error) {
	var dbTime time.Time
	err := s.getDB(ctx).Raw("SELECT now()").Scan(&dbTime).Error
	return dbTime, err
}

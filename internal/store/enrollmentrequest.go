package store

import (
	"github.com/flightctl/flightctl/internal/model"
	"gorm.io/gorm"
)

type EnrollmentRequestStore struct {
	db *gorm.DB
}

func NewEnrollmentRequestStoreStore(db *gorm.DB) *EnrollmentRequestStore {
	return &EnrollmentRequestStore{db: db}
}

func (s *EnrollmentRequestStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.EnrollmentRequest{})
}

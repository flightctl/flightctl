package model

import (
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID uuid.UUID `gorm:"type:uuid;primary_key"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

package store

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RemoveOwnerCallback func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error

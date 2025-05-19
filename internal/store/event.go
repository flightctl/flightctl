package store

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Event interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, event *api.Event) error
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EventList, error)
	DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error)
}

type EventStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Event, model.Event, api.Event, api.EventList]
}

// Make sure we conform to Event interface
var _ Event = (*EventStore)(nil)

func NewEvent(db *gorm.DB, log logrus.FieldLogger) Event {
	genericStore := NewGenericStore[*model.Event, model.Event, api.Event, api.EventList](
		db,
		log,
		model.NewEventFromApiResource,
		(*model.Event).ToApiResource,
		model.EventsToApiResource,
	)
	return &EventStore{db: db, log: log, genericStore: genericStore}
}

func (s *EventStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Event{}); err != nil {
		return err
	}

	return nil
}

func (s *EventStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Event) error {
	m, _ := model.NewEventFromApiResource(resource)
	m.OrgID = orgId
	return s.db.WithContext(ctx).Create(&m).Error
}

func (s *EventStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EventList, error) {
	return s.genericStore.List(ctx, orgId, listParams, lo.ToPtr("created_at desc"))
}

// DeleteEventsOlderThan deletes events older than the provided timestamp
func (s *EventStore) DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error) {
	// Delete events older than the cutoff time
	result := s.db.WithContext(ctx).Unscoped().Where("created_at < ?", cutoffTime).Delete(&model.Event{})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete events: %w", result.Error)
	}

	return result.RowsAffected, nil
}

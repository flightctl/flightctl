package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Event interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, event *api.Event) error
	List(ctx context.Context, orgId uuid.UUID, listParams ListEventsParams) (*api.EventList, error)
	DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error)
}

type EventStore struct {
	db *gorm.DB
}

// Make sure we conform to Event interface
var _ Event = (*EventStore)(nil)

func NewEvent(db *gorm.DB) Event {
	return &EventStore{db: db}
}

func (s *EventStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Event{}); err != nil {
		return err
	}

	// Create a BRIN index for event timestamps
	if !s.db.Migrator().HasIndex(&model.Event{}, "idx_event_timestamp_brin") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_event_timestamp_brin ON events USING BRIN (timestamp)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Event{}, "idx_event_timestamp_brin"); err != nil {
				return err
			}
		}
	}

	// Create a composite index on resource_kind and resource_name
	if err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_event_resource_kind_name ON events (resource_kind, resource_name)").Error; err != nil {
		return fmt.Errorf("failed to create composite index: %w", err)
	}

	return nil
}

func (s *EventStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Event) error {
	m := model.NewEventFromApiResource(resource)
	m.OrgID = orgId
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now().UTC()
	}
	return s.db.WithContext(ctx).Create(&m).Error
}

// List fetches events with filters and pagination
func (s *EventStore) List(ctx context.Context, orgId uuid.UUID, listParams ListEventsParams) (*api.EventList, error) {
	var events []model.Event

	// Start query with base conditions
	query := s.db.Model(&model.Event{}).Where("org_id = ?", orgId)

	// Apply filters
	query = applyFilters(query, listParams)

	// Apply pagination: Decode Continue Token
	if listParams.Continue != nil {
		query = query.Where("id <= ?", listParams.Continue)
	}

	// Fetch limit+1 to check if there are more events
	query = query.Order("id DESC").Limit(listParams.Limit + 1)

	// Execute query
	err := query.Find(&events).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}

	// Determine "Continue" token & Remaining Count
	var nextContinue *string
	var numRemaining *int64

	if len(events) > listParams.Limit {
		// More events exist, so set the continue token and trim results
		lastEvent := events[listParams.Limit]
		nextToken := strconv.FormatUint(lastEvent.ID, 10)
		nextContinue = &nextToken
		events = events[:listParams.Limit] // Trim to requested limit

		// Count remaining items
		var remaining int64
		countQuery := s.db.WithContext(ctx).Model(&model.Event{}).Where("org_id = ?", orgId)
		countQuery = applyFilters(countQuery, listParams)
		countQuery = countQuery.Where("id <= ?", lastEvent.ID)

		if err := countQuery.Count(&remaining).Error; err != nil {
			return nil, ErrorFromGormError(err)
		}
		numRemaining = &remaining
	}

	apiList, err := model.EventsToApiResource(events, nextContinue, numRemaining)
	return &apiList, err
}

// Apply filters for both list and count queries
func applyFilters(query *gorm.DB, listParams ListEventsParams) *gorm.DB {
	if listParams.Kind != nil {
		query = query.Where("resource_kind = ?", *listParams.Kind)
	}
	if listParams.Name != nil {
		query = query.Where("resource_name = ?", *listParams.Name)
	}
	if listParams.CorrelationId != nil {
		query = query.Where("correlation_id = ?", *listParams.CorrelationId)
	}
	if listParams.Severity != nil {
		query = query.Where("severity = ?", *listParams.Severity)
	}
	if listParams.StartTime != nil {
		query = query.Where("timestamp >= ?", *listParams.StartTime)
	}
	if listParams.EndTime != nil {
		query = query.Where("timestamp <= ?", *listParams.EndTime)
	}
	return query
}

// DeleteEventsOlderThan deletes events older than the provided timestamp
func (s *EventStore) DeleteOlderThan(ctx context.Context, cutoffTime time.Time) (int64, error) {
	// Delete events older than the cutoff time
	result := s.db.WithContext(ctx).Unscoped().Where("timestamp < ?", cutoffTime).Delete(&model.Event{})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete events: %w", result.Error)
	}

	return result.RowsAffected, nil
}

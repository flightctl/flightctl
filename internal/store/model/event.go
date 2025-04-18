package model

import (
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Event struct {
	ID            uint64                       `gorm:"primaryKey;autoIncrement"`
	OrgID         uuid.UUID                    `gorm:"type:uuid;primaryKey;index"`
	Timestamp     time.Time                    `gorm:"autoCreateTime"` // No index - BRIN index created in InitialMigration
	EventType     string                       `gorm:"type:varchar(100);index"`
	Source        string                       `gorm:"type:varchar(100)"`
	ActorUser     *string                      `gorm:"type:varchar(100);nullable"`
	ActorService  *string                      `gorm:"type:varchar(100);nullable"`
	Status        string                       `gorm:"type:varchar(20);index"`
	Severity      string                       `gorm:"type:varchar(20);index"`
	Message       string                       `gorm:"type:text"`
	Details       *JSONField[api.EventDetails] `gorm:"type:jsonb"`
	CorrelationID *string                      `gorm:"type:varchar(100);nullable"`
	ResourceName  string                       `gorm:"index"`
	ResourceKind  string                       `gorm:"type:varchar(50);index"`
	CreatedAt     time.Time                    `gorm:"autoCreateTime"`
	DeletedAt     gorm.DeletedAt               `gorm:"index"`
}

func (e Event) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewEventFromApiResource(resource *api.Event) *Event {
	if resource == nil {
		return &Event{}
	}
	details := api.EventDetails{}
	if resource.Details != nil {
		details = *resource.Details
	}
	return &Event{
		Timestamp:     resource.Timestamp,
		EventType:     string(resource.Type),
		Source:        string(resource.Source),
		ActorUser:     resource.ActorUser,
		ActorService:  resource.ActorService,
		Status:        string(resource.Status),
		Severity:      string(resource.Severity),
		Message:       resource.Message,
		Details:       MakeJSONField(details),
		CorrelationID: resource.CorrelationId,
		ResourceName:  resource.ResourceName,
		ResourceKind:  string(resource.ResourceKind),
	}
}

func EventAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.EventAPIVersion)
}

func (e *Event) ToApiResource(opts ...APIResourceOption) (*api.Event, error) {
	if e == nil {
		return &api.Event{}, nil
	}

	var details *api.EventDetails
	if e.Details != nil {
		details = &e.Details.Data
	}

	return &api.Event{
		ApiVersion:    EventAPIVersion(),
		Kind:          api.EventKind,
		Id:            e.ID,
		Type:          api.EventType(e.EventType),
		Source:        api.EventSource(e.Source),
		ActorUser:     e.ActorUser,
		ActorService:  e.ActorService,
		Status:        api.EventStatus(e.Status),
		Severity:      api.EventSeverity(e.Severity),
		Message:       e.Message,
		Details:       details,
		CorrelationId: e.CorrelationID,
		ResourceName:  e.ResourceName,
		ResourceKind:  api.ResourceKind(e.ResourceKind),
		Timestamp:     e.Timestamp,
	}, nil
}

func EventsToApiResource(events []Event, cont *string, numRemaining *int64) (api.EventList, error) {
	eventList := make([]api.Event, len(events))
	for i, event := range events {
		var opts []APIResourceOption
		apiResource, _ := event.ToApiResource(opts...)
		eventList[i] = *apiResource
	}
	ret := api.EventList{
		ApiVersion: EventAPIVersion(),
		Kind:       api.EventListKind,
		Items:      eventList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

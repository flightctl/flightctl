package model

import (
	"encoding/json"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/samber/lo"
)

type Event struct {
	Resource
	Reason             string                       `gorm:"type:string;index" selector:"reason"`
	SourceComponent    string                       `gorm:"type:string"`
	Actor              string                       `gorm:"type:string" selector:"actor"`
	Type               string                       `gorm:"type:string;index" selector:"type"`
	Message            string                       `gorm:"type:text"`
	Details            *JSONField[api.EventDetails] `gorm:"type:jsonb"`
	InvolvedObjectName string                       `gorm:"type:string;index:idx_involved_object" selector:"involvedObject.name"`
	InvolvedObjectKind string                       `gorm:"type:string;index:idx_involved_object" selector:"involvedObject.kind"`
}

func (e Event) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewEventFromApiResource(resource *api.Event) (*Event, error) {
	if resource == nil {
		return &Event{}, nil
	}
	details := api.EventDetails{}
	if resource.Details != nil {
		details = *resource.Details
	}
	return &Event{
		Resource: Resource{
			Name: *resource.Metadata.Name,
		},
		Reason:             string(resource.Reason),
		SourceComponent:    resource.Source.Component,
		Actor:              resource.Actor,
		Type:               string(resource.Type),
		Message:            resource.Message,
		Details:            MakeJSONField(details),
		InvolvedObjectName: resource.InvolvedObject.Name,
		InvolvedObjectKind: resource.InvolvedObject.Kind,
	}, nil
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
		ApiVersion: EventAPIVersion(),
		Kind:       api.EventKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(e.Name),
			CreationTimestamp: lo.ToPtr(e.CreatedAt.UTC()),
		},
		InvolvedObject: api.ObjectReference{
			Kind: e.InvolvedObjectKind,
			Name: e.InvolvedObjectName,
		},
		Reason: api.EventReason(e.Reason),
		Source: api.EventSource{
			Component: e.SourceComponent,
		},
		Actor:   e.Actor,
		Type:    api.EventType(e.Type),
		Message: e.Message,
		Details: details,
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

func (e *Event) GetKind() string {
	return api.EventKind
}

func (e *Event) HasNilSpec() bool {
	return true
}

func (e *Event) HasSameSpecAs(otherResource any) bool {
	return true
}

func (e *Event) GetStatusAsJson() ([]byte, error) {
	return nil, nil
}

package deltaprepare

import (
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// PrepareCompletionPayload identifies a completed prepare that must resume
// its resource. It carries the resource identity needed by the completion
// task and does not require a prepare-row lookup.
type PrepareCompletionPayload struct {
	Kind                  string  `json:"kind"`
	Name                  string  `json:"name"`
	TemplateVersion       *string `json:"templateVersion,omitempty"`
	SpecHash              *string `json:"specHash,omitempty"`
	SourceResourceVersion int64   `json:"sourceResourceVersion"`
}

// NewPrepareCompletionEvent builds an internal notification for the
// completion task. The event is emitted after the prepare state transaction
// commits, so the completion task can safely apply the resource-side effects.
func NewPrepareCompletionEvent(prepare *model.DeltaPrepare) (*domain.Event, error) {
	if prepare == nil {
		return nil, fmt.Errorf("prepare is required")
	}
	if prepare.Kind == "" || prepare.Name == "" {
		return nil, fmt.Errorf("prepare kind and name are required")
	}
	if prepare.SourceResourceVersion <= 0 {
		return nil, fmt.Errorf("prepare source resource version must be positive")
	}
	payload := PrepareCompletionPayload{
		Kind:                  prepare.Kind,
		Name:                  prepare.Name,
		TemplateVersion:       prepare.TemplateVersion,
		SpecHash:              prepare.SpecHash,
		SourceResourceVersion: prepare.SourceResourceVersion,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal prepare completion payload: %w", err)
	}
	return &domain.Event{
		Reason:  domain.EventReasonDeltaPrepareComplete,
		Message: string(payloadBytes),
	}, nil
}

// ParsePrepareCompletionEvent validates an internal prepare-completion
// notification and converts it into the prepare identity consumed by the
// completion service.
func ParsePrepareCompletionEvent(orgID uuid.UUID, message string) (*model.DeltaPrepare, error) {
	var payload PrepareCompletionPayload
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return nil, fmt.Errorf("parse prepare completion payload: %w", err)
	}
	if payload.Kind == "" || payload.Name == "" {
		return nil, fmt.Errorf("prepare completion payload is missing kind or name")
	}
	switch payload.Kind {
	case domain.FleetKind, domain.DeviceKind:
	default:
		return nil, fmt.Errorf("prepare completion payload has unsupported kind %q", payload.Kind)
	}
	if payload.SourceResourceVersion <= 0 {
		return nil, fmt.Errorf("prepare completion payload has invalid source resource version")
	}
	return &model.DeltaPrepare{
		OrgID:                 orgID,
		Kind:                  payload.Kind,
		Name:                  payload.Name,
		TemplateVersion:       payload.TemplateVersion,
		SpecHash:              payload.SpecHash,
		SourceResourceVersion: payload.SourceResourceVersion,
		Status:                model.DeltaPrepareComplete,
	}, nil
}

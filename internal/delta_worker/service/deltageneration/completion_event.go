package deltageneration

import (
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// GenerationCompletePayload is the internal queue payload emitted after a
// generation reaches a terminal state. The generation key is deliberately the
// only resource identity carried here: one generation can be shared by many
// prepares, so the completion service must fan out from the join table.
type GenerationCompletePayload struct {
	ImageRepository string `json:"imageRepository"`
	SourceDigest    string `json:"sourceDigest"`
	TargetDigest    string `json:"targetDigest"`
	Status          string `json:"status"`
}

// NewGenerationCompleteEvent builds the internal notification emitted after
// the terminal generation update has been committed.
func NewGenerationCompleteEvent(generation *model.DeltaGeneration) (*domain.Event, error) {
	if generation == nil {
		return nil, fmt.Errorf("generation is required")
	}
	if !isTerminalStatus(generation.Status) {
		return nil, fmt.Errorf("generation status %q is not terminal", generation.Status)
	}
	payload, err := json.Marshal(GenerationCompletePayload{
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
		Status:          generation.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal generation complete payload: %w", err)
	}
	return &domain.Event{
		Reason:  domain.EventReasonDeltaGenerationComplete,
		Message: string(payload),
	}, nil
}

// ParseGenerationCompleteEvent validates an internal terminal-generation
// notification and returns the generation key and public progress status.
func ParseGenerationCompleteEvent(orgID uuid.UUID, message string) (deltastore.GenerationKey, domain.DeltaGenerationProgressDetailsGenerationStatus, error) {
	var payload GenerationCompletePayload
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return deltastore.GenerationKey{}, "", fmt.Errorf("parse generation complete payload: %w", err)
	}
	if payload.ImageRepository == "" || payload.SourceDigest == "" || payload.TargetDigest == "" {
		return deltastore.GenerationKey{}, "", fmt.Errorf("generation complete payload is missing a generation key")
	}
	if !isTerminalStatus(payload.Status) {
		return deltastore.GenerationKey{}, "", fmt.Errorf("generation status %q is not terminal", payload.Status)
	}
	return deltastore.GenerationKey{
		OrgID:           orgID,
		ImageRepository: payload.ImageRepository,
		SourceDigest:    payload.SourceDigest,
		TargetDigest:    payload.TargetDigest,
	}, domain.DeltaGenerationProgressDetailsGenerationStatus(payload.Status), nil
}

func isTerminalStatus(status string) bool {
	return status == model.DeltaGenerationSucceeded || status == model.DeltaGenerationFailed || status == model.DeltaGenerationRejected
}

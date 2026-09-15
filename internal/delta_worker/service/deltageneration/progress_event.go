package deltageneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
)

const (
	progressSource = "flightctl-delta-worker"
	progressActor  = "service:flightctl-delta-worker"
)

func DeltaGenerationProgressEvent(ctx context.Context, prepare model.DeltaPrepare, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) (*domain.Event, error) {
	if status != domain.DeltaGenerationProgressInProgress {
		phase = nil
	}
	details := domain.DeltaGenerationProgressDetails{
		DetailType:       domain.DeltaGenerationProgress,
		ImageRepository:  key.ImageRepository,
		SourceDigest:     key.SourceDigest,
		TargetDigest:     key.TargetDigest,
		GenerationStatus: status,
		Phase:            phase,
	}
	if prepare.Kind == domain.FleetKind {
		details.TemplateVersion = prepare.TemplateVersion
	}
	if prepare.Kind == domain.DeviceKind {
		details.SpecHash = prepare.SpecHash
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromDeltaGenerationProgressDetails(details); err != nil {
		return nil, err
	}
	event := domain.GetBaseEvent(ctx, domain.ResourceKind(prepare.Kind), prepare.Name, domain.EventReasonDeltaGenerationProgress, progressMessage(key, status, phase), &eventDetails)
	event.Source.Component = progressSource
	event.Actor = progressActor
	if status == domain.DeltaGenerationProgressFailed {
		event.Type = domain.EventTypeWarning
	}
	return event, nil
}

func progressMessage(key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) string {
	pair := fmt.Sprintf("%s %s → %s", key.ImageRepository, key.SourceDigest, key.TargetDigest)
	if status == domain.DeltaGenerationProgressInProgress && phase != nil && *phase != "" {
		return fmt.Sprintf("Delta generation for %s entered %s.", pair, *phase)
	}
	return fmt.Sprintf("Delta generation for %s %s.", pair, status)
}

func GenerationPhasePtr(generation *model.DeltaGeneration) *domain.DeltaGenerationPhase {
	if generation == nil || generation.Phase == nil || *generation.Phase == "" {
		return nil
	}
	phase := domain.DeltaGenerationPhase(*generation.Phase)
	return &phase
}

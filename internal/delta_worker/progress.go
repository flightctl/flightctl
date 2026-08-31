package delta_worker

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
)

const (
	progressSource = "flightctl-delta-worker"
	progressActor  = "service:flightctl-delta-worker"
)

func deltaGenerationProgressEvent(ctx context.Context, prep model.DeltaPrepare, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) (*domain.Event, error) {
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
	if prep.Kind == domain.FleetKind {
		details.TemplateVersion = prep.TemplateVersion
	}
	if prep.Kind == domain.DeviceKind {
		details.SpecResourceVersion = prep.SpecResourceVersion
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromDeltaGenerationProgressDetails(details); err != nil {
		return nil, err
	}
	event := domain.GetBaseEvent(ctx, domain.ResourceKind(prep.Kind), prep.Name, domain.EventReasonDeltaGenerationProgress, progressMessage(key, status, phase), &eventDetails)
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

func generationPhasePtr(gen *model.DeltaGeneration) *domain.DeltaGenerationPhase {
	if gen == nil || gen.Phase == nil || *gen.Phase == "" {
		return nil
	}
	p := domain.DeltaGenerationPhase(*gen.Phase)
	return &p
}

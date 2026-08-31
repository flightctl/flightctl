package delta_worker

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
)

func deltaGenerationProgressEvent(ctx context.Context, prep model.DeltaPrepare, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) (*domain.Event, error) {
	details := domain.DeltaGenerationProgressDetails{
		DetailType:          domain.DeltaGenerationProgress,
		ImageRepository:     key.ImageRepository,
		SourceDigest:        key.SourceDigest,
		TargetDigest:        key.TargetDigest,
		GenerationStatus:    status,
		Phase:               phase,
		TemplateVersion:     prep.TemplateVersion,
		SpecResourceVersion: prep.SpecResourceVersion,
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromDeltaGenerationProgressDetails(details); err != nil {
		return nil, err
	}
	event := domain.GetBaseEvent(ctx, domain.ResourceKind(prep.Kind), prep.Name, domain.EventReasonDeltaGenerationProgress, progressMessage(key, status, phase), &eventDetails)
	if status == domain.DeltaGenerationProgressFailed {
		event.Type = domain.EventTypeWarning
	}
	return event, nil
}

func progressMessage(key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) string {
	if status == domain.DeltaGenerationProgressInProgress && phase != nil && *phase != "" {
		return fmt.Sprintf("Delta generation %s for %s", *phase, key.ImageRepository)
	}
	return fmt.Sprintf("Delta generation %s for %s", status, key.ImageRepository)
}

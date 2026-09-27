package deltaprepare

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
)

// CompleteWaitingIfTerminal atomically advances prepares waiting on the
// terminal generation and returns their grouped progress. Resource-side status
// updates belong to the generation-complete task handler, which owns this
// completion event.
func (h *ServiceHandler) CompleteWaitingIfTerminal(ctx context.Context, key deltagenerationstore.GenerationKey, expectedStatus string) ([]deltapreparestore.PrepareProgress, error) {
	progress, err := h.store.DecrementPendingGenerationsForGeneration(ctx, key, expectedStatus)
	if err != nil {
		return nil, fmt.Errorf("decrement pending generations for generation: %w", err)
	}
	return progress, nil
}

// EmitTerminalGenerationProgress emits the public per-generation progress
// event for a prepare claimed by the generation-complete flow. The caller has
// already performed the fan-out, so this does not look up linked prepares.
func (h *ServiceHandler) EmitTerminalGenerationProgress(
	ctx context.Context,
	key deltagenerationstore.GenerationKey,
	status domain.DeltaGenerationProgressDetailsGenerationStatus,
	prepare *model.DeltaPrepare,
) error {
	if prepare == nil {
		return nil
	}
	event, err := deltageneration.DeltaGenerationProgressEvent(ctx, *prepare, key, status, nil)
	if err != nil {
		return fmt.Errorf("create terminal generation progress event: %w", err)
	}
	return h.createEvent(ctx, prepare.OrgID, event)
}

// ResumeCompletedPrepare applies the resource-owned resume mutation when the
// resource still matches the prepare, then emits the completion event. A
// conditional update that affects no rows means the completion is stale or
// already handled, so processing stops without emitting an event.
func (h *ServiceHandler) ResumeCompletedPrepare(ctx context.Context, prepare *model.DeltaPrepare) error {
	if prepare == nil {
		return nil
	}
	result, err := h.status.ResumeIfCurrent(ctx, prepare.OrgID, prepare.Kind, prepare.Name, workerservice.ResumeIdentityForPrepare(prepare))
	if err != nil {
		return err
	}
	if !result.Matched {
		return nil
	}
	if err := h.emitResume(ctx, prepare.OrgID, prepare.Kind, prepare, result.Fleet); err != nil {
		return err
	}
	return nil
}

func (h *ServiceHandler) emitResume(ctx context.Context, orgID uuid.UUID, kind string, prepare *model.DeltaPrepare, fleet *domain.Fleet) error {
	switch kind {
	case domain.FleetKind:
		return h.emitFleetResume(ctx, orgID, prepare, fleet)
	case domain.DeviceKind:
		return h.createEvent(ctx, orgID, domain.GetBaseEvent(ctx, domain.DeviceKind, prepare.Name, domain.EventReasonDeltaGenerationCompleted, "Delta generation completed.", nil))
	default:
		return fmt.Errorf("unsupported prepare kind %q", kind)
	}
}

func (h *ServiceHandler) emitFleetResume(ctx context.Context, orgID uuid.UUID, prepare *model.DeltaPrepare, fleet *domain.Fleet) error {
	if fleet == nil {
		return fmt.Errorf("fleet resource is required after conditional resume")
	}
	if prepare == nil || prepare.TemplateVersion == nil || *prepare.TemplateVersion == "" {
		return fmt.Errorf("fleet template version is required after conditional resume")
	}
	immediate := fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil
	return h.createEvent(ctx, orgID, common.GetFleetRolloutStartedEvent(ctx, *prepare.TemplateVersion, prepare.Name, immediate, false))
}

type reliableEventService interface {
	CreateEventWithRetry(context.Context, uuid.UUID, *domain.Event) error
}

func (h *ServiceHandler) createEvent(ctx context.Context, orgID uuid.UUID, event *domain.Event) error {
	if event == nil {
		return nil
	}
	if reliable, ok := h.events.(reliableEventService); ok {
		return reliable.CreateEventWithRetry(ctx, orgID, event)
	}
	h.events.CreateEvent(ctx, orgID, event)
	return nil
}

package generationcomplete

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
)

// InvalidPayloadError marks a generation-complete message that cannot be
// retried. The queue consumer acknowledges these messages as permanent
// failures instead of sending them back through the retry loop.
type InvalidPayloadError struct {
	err error
}

func (e *InvalidPayloadError) Error() string {
	return e.err.Error()
}

func (e *InvalidPayloadError) Unwrap() error {
	return e.err
}

// IsInvalidPayload reports whether err represents a malformed task message.
func IsInvalidPayload(err error) bool {
	var invalid *InvalidPayloadError
	return errors.As(err, &invalid)
}

// Handler processes terminal-generation notifications from the delta-worker
// queue. It owns the resource-side progress transition for this completion
// event; the prepare service only advances the database counter and performs
// the final conditional resume.
type Handler struct {
	completion *deltaprepare.ServiceHandler
	status     *workerservice.StorePreparingStatus
}

// NewHandler creates a generation-complete task handler.
func NewHandler(completion *deltaprepare.ServiceHandler, status *workerservice.StorePreparingStatus) (*Handler, error) {
	if completion == nil {
		return nil, fmt.Errorf("completion service is required")
	}
	if status == nil {
		return nil, fmt.Errorf("preparing status service is required")
	}
	return &Handler{completion: completion, status: status}, nil
}

// Handle validates a terminal-generation notification and completes prepares
// waiting on the referenced generation.
func (h *Handler) Handle(ctx context.Context, event worker_client.EventWithOrgId) error {
	if event.Event.Reason != domain.EventReasonDeltaGenerationComplete {
		return nil
	}
	key, status, err := deltageneration.ParseGenerationCompleteEvent(event.OrgId, event.Event.Message)
	if err != nil {
		return &InvalidPayloadError{err: fmt.Errorf("invalid DeltaGenerationComplete payload: %w", err)}
	}
	progress, err := h.completion.CompleteWaitingIfTerminal(ctx, key)
	if err != nil {
		return err
	}
	var updateErrors []error
	for i := range progress {
		prepare := &progress[i].Prepare
		if err := h.completion.EmitTerminalGenerationProgress(ctx, key, status, prepare); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("emit progress for prepare %s: %w", prepare.ID, err))
		}
		if prepare.Status == model.DeltaPrepareComplete {
			if err := h.completion.ResumeCompletedPrepare(ctx, prepare); err != nil {
				updateErrors = append(updateErrors, fmt.Errorf("resume completed prepare %s: %w", prepare.ID, err))
			}
			continue
		}
		if err := h.status.Set(ctx, prepare.OrgID, prepare.Kind, prepare.Name, progress[i].Completed, progress[i].Total); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("set progress for prepare %s: %w", prepare.ID, err))
		}
	}
	return errors.Join(updateErrors...)
}

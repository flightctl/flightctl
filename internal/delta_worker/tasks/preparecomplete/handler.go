package preparecomplete

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
)

// InvalidPayloadError marks a prepare-completion message that cannot be
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

// Handler processes notifications for prepares that are ready to resume.
type Handler struct {
	completion *deltaprepare.ServiceHandler
}

// NewHandler creates a prepare-completion task handler.
func NewHandler(completion *deltaprepare.ServiceHandler) (*Handler, error) {
	if completion == nil {
		return nil, fmt.Errorf("completion service is required")
	}
	return &Handler{completion: completion}, nil
}

// Handle validates a prepare-completion notification and delegates the
// resource-side resume to the completion service.
func (h *Handler) Handle(ctx context.Context, event worker_client.EventWithOrgId) error {
	if event.Event.Reason != domain.EventReasonDeltaPrepareComplete {
		return nil
	}
	prepare, err := deltaprepare.ParsePrepareCompletionEvent(event.OrgId, event.Event.Message)
	if err != nil {
		return &InvalidPayloadError{err: fmt.Errorf("invalid DeltaPrepareComplete payload: %w", err)}
	}
	return h.completion.ResumeCompletedPrepare(ctx, prepare)
}

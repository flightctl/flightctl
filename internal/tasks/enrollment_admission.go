package tasks

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type EnrollmentReevaluator interface {
	ReevaluateEnrollment(context.Context, uuid.UUID, domain.Event) error
}

func shouldReevaluateEnrollment(event domain.Event) bool {
	switch event.InvolvedObject.Kind {
	case domain.EnrollmentRequestKind:
		return event.Reason == domain.EventReasonResourceCreated || event.Reason == domain.EventReasonResourceUpdated
	case domain.EnrollmentApprovalPolicyKind:
		return event.Reason == domain.EventReasonResourceCreated || event.Reason == domain.EventReasonResourceUpdated || event.Reason == domain.EventReasonResourceDeleted
	default:
		return false
	}
}

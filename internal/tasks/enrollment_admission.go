package tasks

import "github.com/flightctl/flightctl/internal/domain"

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

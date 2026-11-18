package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/google/uuid"
)

func (h *ServiceHandler) updateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	if resource == nil || resource.Metadata.Name == nil || resource.Status == nil {
		return nil, nil, flterrors.ErrResourceIsNil
	}
	updated, previous, _, err := h.store.Mutate(ctx, orgId, *resource.Metadata.Name, nil, func(mutation *enrollmentrequeststore.EnrollmentRequestMutation) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.EnrollmentRequest
		status := *resource.Status
		if current.Status != nil {
			status.EnrollmentCertificateName = current.Status.EnrollmentCertificateName
			if !enrollmentrequeststore.Pending(current) {
				status.Certificate = current.Status.Certificate
				status.Approval = current.Status.Approval
				for _, conditionType := range []domain.ConditionType{domain.ConditionTypeEnrollmentRequestApproved, domain.ConditionTypeEnrollmentRequestDenied} {
					if condition := domain.FindStatusCondition(current.Status.Conditions, conditionType); condition != nil {
						domain.SetStatusCondition(&status.Conditions, *condition)
					}
				}
			}
			if denied := domain.FindStatusCondition(current.Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied); denied != nil &&
				domain.FindStatusCondition(status.Conditions, domain.ConditionTypeEnrollmentRequestDenied) == nil {
				domain.SetStatusCondition(&status.Conditions, *denied)
			}
		}
		current.Status = &status
		return nil
	})
	return updated, previous, store.ErrorFromGormError(err)
}

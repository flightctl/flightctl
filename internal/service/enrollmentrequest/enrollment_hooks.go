package enrollmentrequest

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	enrollmenthookpolicy "github.com/flightctl/flightctl/internal/service/enrollmenthookpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// snapshotEnrollmentHookPolicy looks up the default policy and, if found,
// builds the snapshot from policy.Spec.AfterEnrolling. Returns the snapshot
// for Device status and the secrets to store.
// Returns (nil, nil, nil) when no policy exists.
func snapshotEnrollmentHookPolicy(
	ctx context.Context,
	policySvc enrollmenthookpolicy.Service,
	orgId uuid.UUID,
	deviceName string,
) (*domain.DeviceEnrollmentHooksStatus, []model.EnrollmentHookNotifySecret, error) {
	if policySvc == nil {
		return nil, nil, nil
	}

	policy, getStatus := policySvc.GetEnrollmentHookPolicy(ctx, orgId, "default")
	if getStatus.Code == http.StatusNotFound {
		return nil, nil, nil
	}
	if getStatus.Code != http.StatusOK {
		return nil, nil, fmt.Errorf("get enrollment hook policy: %s", getStatus.Message)
	}

	snapshot := domain.EnrollmentHookSnapshot{
		FailurePolicy: lo.FromPtr(policy.Spec.AfterEnrolling.FailurePolicy),
	}

	var secrets []model.EnrollmentHookNotifySecret
	if policy.Spec.AfterEnrolling.ControlPlaneActions != nil {
		actions := make([]domain.EnrollmentHookSnapshotAction, 0, len(*policy.Spec.AfterEnrolling.ControlPlaneActions))
		for i, action := range *policy.Spec.AfterEnrolling.ControlPlaneActions {
			snapshotAction := domain.EnrollmentHookSnapshotAction{
				Index:   i,
				Url:     action.Url,
				Timeout: action.Timeout,
				Retry:   action.Retry,
			}
			actions = append(actions, snapshotAction)

			if action.Auth != nil && action.Auth.BearerToken != nil && *action.Auth.BearerToken != "" {
				secrets = append(secrets, model.EnrollmentHookNotifySecret{
					DeviceName:  deviceName,
					ActionIndex: i,
					BearerToken: *action.Auth.BearerToken,
				})
			}
		}
		snapshot.ControlPlaneActions = &actions
	}

	hooksStatus := &domain.DeviceEnrollmentHooksStatus{
		Snapshot: &snapshot,
	}
	return hooksStatus, secrets, nil
}

// enrollmentHooksConditionReason returns the initial condition reason based
// on whether the snapshot has notify actions (controlPlaneActions).
func enrollmentHooksConditionReason(snapshot *domain.EnrollmentHookSnapshot) string {
	if snapshot.ControlPlaneActions != nil && len(*snapshot.ControlPlaneActions) > 0 {
		return domain.EnrollmentHooksReasonNotifyPending
	}
	return domain.EnrollmentHooksReasonPending
}

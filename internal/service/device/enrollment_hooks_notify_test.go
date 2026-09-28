package device

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestEnrollmentHooksConditionChanged(t *testing.T) {
	testCases := []struct {
		name          string
		oldConditions []domain.Condition
		newConditions []domain.Condition
		want          bool
	}{
		{
			name: "When both absent it should not change",
			want: false,
		},
		{
			name: "When condition is added it should change",
			newConditions: []domain.Condition{{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusFalse,
				Reason: domain.EnrollmentHooksReasonNotifyPending,
			}},
			want: true,
		},
		{
			name: "When reason changes it should change",
			oldConditions: []domain.Condition{{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusFalse,
				Reason: domain.EnrollmentHooksReasonNotifyPending,
			}},
			newConditions: []domain.Condition{{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusFalse,
				Reason: domain.EnrollmentHooksReasonPending,
			}},
			want: true,
		},
		{
			name: "When only unrelated conditions change it should not change",
			oldConditions: []domain.Condition{{
				Type:   domain.ConditionTypeDeviceEnrollmentHooks,
				Status: domain.ConditionStatusFalse,
				Reason: domain.EnrollmentHooksReasonPending,
			}},
			newConditions: []domain.Condition{
				{
					Type:   domain.ConditionTypeDeviceEnrollmentHooks,
					Status: domain.ConditionStatusFalse,
					Reason: domain.EnrollmentHooksReasonPending,
				},
				{
					Type:   domain.ConditionTypeDeviceSpecValid,
					Status: domain.ConditionStatusTrue,
				},
			},
			want: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, enrollmentHooksConditionChanged(tc.oldConditions, tc.newConditions))
		})
	}
}

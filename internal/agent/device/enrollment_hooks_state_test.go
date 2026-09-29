package device

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentHooksStateFromEvent(t *testing.T) {
	t.Run("When event is nil it should treat condition as absent", func(t *testing.T) {
		state := enrollmentHooksStateFromEvent(nil)
		require.NotNil(t, state)
		assert.True(t, state.conditionAbsent)
		assert.Equal(t, -1, state.condIdx)
		assert.Equal(t, v1beta1.FailurePolicyBlock, state.failurePolicy)
	})

	t.Run("When event has Pending it should map fields", func(t *testing.T) {
		state := enrollmentHooksStateFromEvent(&grpc_v1.EnrollmentHooksEvent{
			Reason:         v1beta1.EnrollmentHooksReasonPending,
			Status:         string(v1beta1.ConditionStatusFalse),
			Message:        "ready",
			ConditionIndex: 2,
			FailurePolicy:  string(v1beta1.FailurePolicyContinue),
			Labels:         map[string]string{"env": "prod"},
		})
		require.NotNil(t, state)
		assert.False(t, state.conditionAbsent)
		assert.Equal(t, v1beta1.EnrollmentHooksReasonPending, state.reason)
		assert.Equal(t, v1beta1.ConditionStatusFalse, state.status)
		assert.Equal(t, "ready", state.message)
		assert.Equal(t, 2, state.condIdx)
		assert.Equal(t, v1beta1.FailurePolicyContinue, state.failurePolicy)
		assert.Equal(t, "prod", state.labels["env"])
	})
}

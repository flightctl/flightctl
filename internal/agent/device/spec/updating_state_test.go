package spec

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestUpdatingState(t *testing.T) {
	require := require.New(t)

	t.Run("initial state", func(t *testing.T) {
		state := &updatingState{}
		condition := state.getCondition()
		require.Nil(condition)
	})

	t.Run("setRetryableError", func(t *testing.T) {
		state := &updatingState{}
		device := newVersionedDevice("1")
		err := fmt.Errorf("retryable error")

		state.setRetryableError(device, err)

		require.Equal(device, state.retryableDevice)
		require.Equal(err, state.retryableError)
		require.Empty(state.failedVersion)
		require.Nil(state.failedError)
		require.Empty(state.policyNotReadyVersion)
		require.Nil(state.policyNextReadyTime)

		condition := state.getCondition()
		require.NotNil(condition)
		require.Equal(v1alpha1.ConditionTypeDeviceUpdating, condition.Type)
		require.Equal(v1alpha1.ConditionStatusTrue, condition.Status)
		require.Equal(string(v1alpha1.UpdateStatePreparing), condition.Reason)
		require.True(strings.Contains(condition.Message, "retryable error"))
	})

	t.Run("setNonRetryableError", func(t *testing.T) {
		state := &updatingState{}
		err := fmt.Errorf("non-retryable error")

		state.setNonRetryableError("1", err)

		require.Nil(state.retryableDevice)
		require.Nil(state.retryableError)
		require.Equal("1", state.failedVersion)
		require.Equal(err, state.failedError)
		require.Empty(state.policyNotReadyVersion)
		require.Nil(state.policyNextReadyTime)

		condition := state.getCondition()
		require.NotNil(condition)
		require.Equal(v1alpha1.ConditionTypeDeviceUpdating, condition.Type)
		require.Equal(v1alpha1.ConditionStatusTrue, condition.Status)
		require.Equal(string(v1alpha1.UpdateStateError), condition.Reason)
		require.True(strings.Contains(condition.Message, "non-retryable error"))
	})

	t.Run("setPolicyNotReady", func(t *testing.T) {
		state := &updatingState{}
		nextReadyTime := time.Now().Add(1 * time.Hour)

		state.setPolicyNotReady("1", &nextReadyTime)

		require.Nil(state.retryableDevice)
		require.Nil(state.retryableError)
		require.Empty(state.failedVersion)
		require.Nil(state.failedError)
		require.Equal("1", state.policyNotReadyVersion)
		require.Equal(&nextReadyTime, state.policyNextReadyTime)

		condition := state.getCondition()
		require.NotNil(condition)
		require.Equal(v1alpha1.ConditionTypeDeviceUpdating, condition.Type)
		require.Equal(v1alpha1.ConditionStatusTrue, condition.Status)
		require.Equal(string(v1alpha1.UpdateStatePreparing), condition.Reason)
		require.True(strings.Contains(condition.Message, "not allowed to proceed"))
	})

	t.Run("clear", func(t *testing.T) {
		state := &updatingState{}
		device := newVersionedDevice("1")
		err := fmt.Errorf("some error")
		nextReadyTime := time.Now()

		state.setRetryableError(device, err)
		state.setNonRetryableError("2", err)
		state.setPolicyNotReady("3", &nextReadyTime)

		state.clear()

		require.Nil(state.retryableDevice)
		require.Nil(state.retryableError)
		require.Empty(state.failedVersion)
		require.Nil(state.failedError)
		require.Empty(state.policyNotReadyVersion)
		require.Nil(state.policyNextReadyTime)
		require.Nil(state.getCondition())
	})
}

package spec

import (
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

// updatingState tracks the current state of device updates for status reporting
// This struct is thread-safe and can be accessed from multiple goroutines
type updatingState struct {
	mutex sync.RWMutex

	// Retryable error state
	retryableDevice *v1alpha1.Device
	retryableError  error

	// Non-retryable error state
	failedVersion string
	failedError   error

	// Policy not ready state
	policyNotReadyVersion string
	policyNextReadyTime   *time.Time
}

// clear resets all state fields
func (u *updatingState) clear() {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	u.retryableDevice = nil
	u.retryableError = nil
	u.failedVersion = ""
	u.failedError = nil
	u.policyNotReadyVersion = ""
	u.policyNextReadyTime = nil
}

// setRetryableError sets the retryable error state and clears other states
func (u *updatingState) setRetryableError(device *v1alpha1.Device, err error) {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	u.retryableDevice = device
	u.retryableError = err
	// Clear other states
	u.failedVersion = ""
	u.failedError = nil
	u.policyNotReadyVersion = ""
	u.policyNextReadyTime = nil
}

// setNonRetryableError sets the non-retryable error state and clears other states
func (u *updatingState) setNonRetryableError(version string, err error) {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	u.failedVersion = version
	u.failedError = err
	// Clear other states
	u.retryableDevice = nil
	u.retryableError = nil
	u.policyNotReadyVersion = ""
	u.policyNextReadyTime = nil
}

// setPolicyNotReady sets the policy not ready state and clears error states
func (u *updatingState) setPolicyNotReady(version string, nextReadyTime *time.Time) {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	u.policyNotReadyVersion = version
	u.policyNextReadyTime = nextReadyTime
	// Clear error states since this is just a policy delay
	u.retryableDevice = nil
	u.retryableError = nil
	u.failedVersion = ""
	u.failedError = nil
}

// getCondition returns the appropriate DeviceUpdating condition based on current state
func (u *updatingState) getCondition() *v1alpha1.Condition {
	u.mutex.RLock()
	defer u.mutex.RUnlock()

	if u.retryableError != nil && u.retryableDevice != nil {
		// Criterion 1: New version consumed with retryable error
		return &v1alpha1.Condition{
			Type:    v1alpha1.ConditionTypeDeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStatePreparing),
			Message: fmt.Sprintf("renderedVersion: %s encountered a retryable error: %v", u.retryableDevice.Version(), u.retryableError),
		}
	} else if u.failedError != nil && u.failedVersion != "" {
		// Criterion 2: New version consumed with non-retryable error
		return &v1alpha1.Condition{
			Type:    v1alpha1.ConditionTypeDeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStateError),
			Message: fmt.Sprintf("renderedVersion: %s failed: %v", u.failedVersion, u.failedError),
		}
	} else if u.policyNotReadyVersion != "" {
		// Criterion 3: Version not ready due to policy
		message := fmt.Sprintf("renderedVersion: %s is not allowed to proceed with updates", u.policyNotReadyVersion)
		if u.policyNextReadyTime != nil {
			message = fmt.Sprintf("%s until %s", message, u.policyNextReadyTime.Format(time.RFC3339))
		}
		return &v1alpha1.Condition{
			Type:    v1alpha1.ConditionTypeDeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStatePreparing),
			Message: message,
		}
	}
	return nil
}

package compliance

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBootcChecker_Status(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	logger := log.NewPrefixLogger("")

	tests := []struct {
		name              string
		timerFileExists   bool
		systemctlOutput   string
		systemctlStderr   string
		systemctlExitCode int
		expectedStatus    v1beta1.ConditionStatus
		expectedReason    string
	}{
		{
			name:            "When timer file does not exist it should be compliant",
			timerFileExists: false,
			expectedStatus:  v1beta1.ConditionStatusTrue,
			expectedReason:  "NotPresent",
		},
		{
			name:              "When timer is masked it should be compliant",
			timerFileExists:   true,
			systemctlOutput:   "masked\n",
			systemctlExitCode: 0,
			expectedStatus:    v1beta1.ConditionStatusTrue,
			expectedReason:    "Masked",
		},
		{
			name:              "When timer is masked-runtime it should be compliant",
			timerFileExists:   true,
			systemctlOutput:   "masked-runtime\n",
			systemctlExitCode: 0,
			expectedStatus:    v1beta1.ConditionStatusTrue,
			expectedReason:    "Masked",
		},
		{
			name:              "When timer is enabled it should be non-compliant",
			timerFileExists:   true,
			systemctlOutput:   "enabled\n",
			systemctlExitCode: 0,
			expectedStatus:    v1beta1.ConditionStatusFalse,
			expectedReason:    "NotMasked",
		},
		{
			name:              "When timer is disabled it should be non-compliant",
			timerFileExists:   true,
			systemctlOutput:   "disabled\n",
			systemctlExitCode: 0,
			expectedStatus:    v1beta1.ConditionStatusFalse,
			expectedReason:    "NotMasked",
		},
		{
			name:              "When timer is static it should be non-compliant",
			timerFileExists:   true,
			systemctlOutput:   "static\n",
			systemctlExitCode: 0,
			expectedStatus:    v1beta1.ConditionStatusFalse,
			expectedReason:    "NotMasked",
		},
		{
			name:              "When systemctl fails with empty output it should be CheckFailed",
			timerFileExists:   true,
			systemctlOutput:   "",
			systemctlStderr:   "Failed to get unit file state",
			systemctlExitCode: 1,
			expectedStatus:    v1beta1.ConditionStatusFalse,
			expectedReason:    "CheckFailed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			setupMocks := func() (*executer.MockExecuter, *fileio.MockReader) {
				mockExec := executer.NewMockExecuter(ctrl)
				mockReader := fileio.NewMockReader(ctrl)

				mockReader.EXPECT().
					PathExists(gomock.Any()).
					Return(tt.timerFileExists, nil).
					AnyTimes()

				if tt.timerFileExists {
					mockExec.EXPECT().
						ExecuteWithContext(ctx, "systemctl", "is-enabled", "bootc-fetch-apply-updates.timer").
						Return(tt.systemctlOutput, tt.systemctlStderr, tt.systemctlExitCode).
						Times(1)
				}

				return mockExec, mockReader
			}

			mockExec, mockReader := setupMocks()
			checker := NewBootcChecker(mockExec, mockReader, logger)

			// Test checkBootcTimer
			condition := checker.checkBootcTimer(ctx)
			require.Equal(tt.expectedStatus, condition.Status)
			require.Equal(tt.expectedReason, condition.Reason)
			require.Equal(v1beta1.ConditionType("BootcTimerCompliant"), condition.Type)
			require.NotEmpty(condition.Message)
		})
	}
}

func TestBootcChecker_StatusUpdatesDeviceConditions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	logger := log.NewPrefixLogger("")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	setupMocks := func() (*executer.MockExecuter, *fileio.MockReader) {
		mockExec := executer.NewMockExecuter(ctrl)
		mockReader := fileio.NewMockReader(ctrl)

		mockReader.EXPECT().
			PathExists(gomock.Any()).
			Return(true, nil).
			Times(1)

		mockExec.EXPECT().
			ExecuteWithContext(ctx, "systemctl", "is-enabled", "bootc-fetch-apply-updates.timer").
			Return("masked\n", "", 0).
			Times(1)

		return mockExec, mockReader
	}

	mockExec, mockReader := setupMocks()
	checker := NewBootcChecker(mockExec, mockReader, logger)

	deviceStatus := &v1beta1.DeviceStatus{
		Conditions: []v1beta1.Condition{
			{
				Type:   v1beta1.ConditionTypeDeviceUpdating,
				Status: v1beta1.ConditionStatusFalse,
			},
		},
	}

	err := checker.Status(ctx, deviceStatus)
	require.NoError(err)

	// Should have added the bootc condition
	require.Len(deviceStatus.Conditions, 2)

	// Find the bootc condition
	bootcCondition := v1beta1.FindStatusCondition(deviceStatus.Conditions, "BootcTimerCompliant")
	require.NotNil(bootcCondition)
	require.Equal(v1beta1.ConditionStatusTrue, bootcCondition.Status)
	require.Equal("Masked", bootcCondition.Reason)
}

func TestUpdateCondition(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name               string
		existingConditions []v1beta1.Condition
		newCondition       v1beta1.Condition
		expectedCount      int
	}{
		{
			name:               "When condition list is empty it should add new condition",
			existingConditions: []v1beta1.Condition{},
			newCondition: v1beta1.Condition{
				Type:   "BootcTimerCompliant",
				Status: v1beta1.ConditionStatusTrue,
			},
			expectedCount: 1,
		},
		{
			name: "When condition exists it should update it",
			existingConditions: []v1beta1.Condition{
				{
					Type:   "BootcTimerCompliant",
					Status: v1beta1.ConditionStatusTrue,
					Reason: "OldReason",
				},
			},
			newCondition: v1beta1.Condition{
				Type:   "BootcTimerCompliant",
				Status: v1beta1.ConditionStatusFalse,
				Reason: "NewReason",
			},
			expectedCount: 1,
		},
		{
			name: "When condition does not exist it should append it",
			existingConditions: []v1beta1.Condition{
				{
					Type:   v1beta1.ConditionTypeDeviceUpdating,
					Status: v1beta1.ConditionStatusFalse,
				},
			},
			newCondition: v1beta1.Condition{
				Type:   "BootcTimerCompliant",
				Status: v1beta1.ConditionStatusTrue,
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := updateCondition(tt.existingConditions, tt.newCondition)
			require.Len(result, tt.expectedCount)

			// Verify the new condition is present with correct values
			found := false
			for _, c := range result {
				if c.Type == tt.newCondition.Type {
					found = true
					require.Equal(tt.newCondition.Status, c.Status)
					require.Equal(tt.newCondition.Reason, c.Reason)
				}
			}
			require.True(found)
		})
	}
}

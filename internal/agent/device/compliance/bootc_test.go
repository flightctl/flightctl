package compliance

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

// mockFileReader implements fileio.Reader for testing
type mockFileReader struct {
	fileExists bool
}

func (m *mockFileReader) ReadFile(name string) ([]byte, error) {
	panic("not implemented")
}

func (m *mockFileReader) ReadDir(name string) ([]os.DirEntry, error) {
	panic("not implemented")
}

func (m *mockFileReader) PathExists(name string, opts ...fileio.PathExistsOption) (bool, error) {
	return m.fileExists, nil
}

func (m *mockFileReader) PathFor(name string) string {
	return name
}

func TestBootcChecker_Status(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	logger := log.NewPrefixLogger("")

	tests := []struct {
		name              string
		timerFileExists   bool
		systemctlOutput   string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock executer
			mockExec := &mockExecuter{
				stdout:   tt.systemctlOutput,
				exitCode: tt.systemctlExitCode,
			}

			// Create mock file reader
			mockReader := &mockFileReader{
				fileExists: tt.timerFileExists,
			}

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

	mockExec := &mockExecuter{
		stdout:   "masked\n",
		exitCode: 0,
	}

	mockReader := &mockFileReader{
		fileExists: true,
	}

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
	var bootcCondition *v1beta1.Condition
	for _, c := range deviceStatus.Conditions {
		if c.Type == "BootcTimerCompliant" {
			bootcCondition = &c
			break
		}
	}

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

// mockExecuter implements executer.Executer for testing
type mockExecuter struct {
	stdout   string
	stderr   string
	exitCode int
}

func (m *mockExecuter) ExecuteWithContext(ctx context.Context, command string, args ...string) (string, string, int) {
	return m.stdout, m.stderr, m.exitCode
}

func (m *mockExecuter) Execute(command string, args ...string) (string, string, int) {
	return m.stdout, m.stderr, m.exitCode
}

func (m *mockExecuter) CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	panic("not implemented")
}

func (m *mockExecuter) ExecuteWithContextFromDir(ctx context.Context, workingDir string, command string, args []string, env ...string) (string, string, int) {
	panic("not implemented")
}

// Ensure we skip the file-exists test on systems where the timer doesn't exist
func TestMain(m *testing.M) {
	// Note: In real testing, we'd want to mock the file system
	// For now, we'll rely on the mock executer and test the logic paths
	os.Exit(m.Run())
}

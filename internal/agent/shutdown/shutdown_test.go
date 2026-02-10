package shutdown

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestParseUtmpRecords(t *testing.T) {
	t.Skip("Test disabled to avoid reading from actual run level path")
	reader := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	testData, err := reader.ReadFile(runlevelPath)
	require.Nil(t, err)
	l := log.NewPrefixLogger("test")
	records := parseUtmpRecords(testData, l)
	require.Nil(t, err)
	require.NotEmpty(t, records)

	var record *utmpRecord
	for _, rec := range records {
		if rec.Type == runlevelRecordType {
			record = &rec
			break
		}
	}

	require.NotNil(t, record, "run level expected")
}

func TestIsShuttingDownViaRunlevelWithTestData(t *testing.T) {
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	testLogger := log.NewPrefixLogger("test")

	// Write test data to a file
	err := rw.WriteFile(runlevelPath, createRunlevelTestData('6'), 0600)
	require.Nil(t, err)

	// Create manager instance
	mgr := &manager{
		log:    testLogger,
		reader: rw,
	}

	result := mgr.isShuttingDownViaRunlevel()
	require.True(t, result)
}

func TestIsShuttingDownViaSystemd(t *testing.T) {
	testCases := []struct {
		name                string
		scheduledFileExists bool
		scheduledFileError  error
		systemctlOutput     string
		systemctlError      error
		systemctlExitCode   int
		expectedResult      bool
	}{
		{
			name:                "scheduled file exists",
			scheduledFileExists: true,
			expectedResult:      true,
		},
		{
			name:                "shutdown.target job running",
			scheduledFileExists: false,
			systemctlOutput:     "123 shutdown.target start running\n",
			systemctlExitCode:   0,
			expectedResult:      true,
		},
		{
			name:                "reboot.target job running",
			scheduledFileExists: false,
			systemctlOutput:     "456 reboot.target start waiting\n",
			systemctlExitCode:   0,
			expectedResult:      true,
		},
		{
			name:                "poweroff.target job running",
			scheduledFileExists: false,
			systemctlOutput:     "789 poweroff.target start running\n",
			systemctlExitCode:   0,
			expectedResult:      true,
		},
		{
			name:                "halt.target job running",
			scheduledFileExists: false,
			systemctlOutput:     "101 halt.target start waiting\n",
			systemctlExitCode:   0,
			expectedResult:      true,
		},
		{
			name:                "no scheduled file, no jobs",
			scheduledFileExists: false,
			systemctlOutput:     "",
			systemctlExitCode:   0,
			expectedResult:      false,
		},
		{
			name:                "shutdown job wrong type",
			scheduledFileExists: false,
			systemctlOutput:     "123 shutdown.target stop running\n",
			systemctlExitCode:   0,
			expectedResult:      false,
		},
		{
			name:                "non-shutdown jobs only",
			scheduledFileExists: false,
			systemctlOutput:     "234 sshd.service start running\n567 nginx.service restart waiting\n",
			systemctlExitCode:   0,
			expectedResult:      false,
		},
		{
			name:                "failed to list jobs",
			scheduledFileExists: false,
			systemctlError:      nil,
			systemctlExitCode:   1,
			expectedResult:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testLogger := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			systemdClient := client.NewSystemd(mockExec, v1beta1.RootUsername)

			// Setup file existence mock
			mockReadWriter.EXPECT().PathExists(shutdownScheduledPath).Return(tc.scheduledFileExists, tc.scheduledFileError)

			// Setup systemctl mock only if scheduled file doesn't exist
			if !tc.scheduledFileExists && tc.scheduledFileError == nil {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"/usr/bin/systemctl",
					[]string{"list-jobs", "--no-pager", "--no-legend"},
				).Return(tc.systemctlOutput, "", tc.systemctlExitCode)
			}

			// Create manager instance
			mgr := &manager{
				log:           testLogger,
				systemdClient: systemdClient,
				reader:        mockReadWriter,
			}

			result := mgr.isShuttingDownViaSystemd(ctx)
			require.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestIsSystemShutdown(t *testing.T) {
	testCases := []struct {
		name                string
		scheduledFileExists bool
		systemctlOutput     string
		systemctlExitCode   int
		utmpData            []byte
		expectedResult      bool
	}{
		{
			name:                "systemd reboot job running",
			scheduledFileExists: false,
			systemctlOutput:     "123 reboot.target start running\n",
			systemctlExitCode:   0,
			expectedResult:      true,
		},
		{
			name:                "runlevel 6 detected",
			scheduledFileExists: false,
			systemctlOutput:     "",
			systemctlExitCode:   0,
			utmpData:            createRunlevelTestData('6'),
			expectedResult:      true,
		},
		{
			name:                "runlevel 0 detected",
			scheduledFileExists: false,
			systemctlOutput:     "",
			systemctlExitCode:   0,
			utmpData:            createRunlevelTestData('0'),
			expectedResult:      true,
		},
		{
			name:                "no shutdown detected",
			scheduledFileExists: false,
			systemctlOutput:     "",
			systemctlExitCode:   0,
			utmpData:            createRunlevelTestData('5'),
			expectedResult:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testLogger := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			systemdClient := client.NewSystemd(mockExec, v1beta1.RootUsername)

			// Setup file system
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			// Write utmp test data
			if tc.utmpData != nil {
				err := rw.WriteFile(runlevelPath, tc.utmpData, 0600)
				require.Nil(t, err)
			}

			// Mock systemctl list-jobs call
			mockExec.EXPECT().ExecuteWithContext(
				gomock.Any(),
				"/usr/bin/systemctl",
				[]string{"list-jobs", "--no-pager", "--no-legend"},
			).Return(tc.systemctlOutput, "", tc.systemctlExitCode)

			// Create manager instance
			mgr := &manager{
				log:           testLogger,
				systemdClient: systemdClient,
				reader:        rw,
			}

			result := mgr.isSystemShutdown(ctx)
			require.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestIsSystemShutdownScheduledFile(t *testing.T) {
	ctx := context.Background()
	testLogger := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	systemdClient := client.NewSystemd(mockExec, v1beta1.RootUsername)

	// Setup file system
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	err := rw.WriteFile(shutdownScheduledPath, []byte{0x00}, 0600)
	require.Nil(t, err)

	// Create manager instance
	mgr := &manager{
		log:           testLogger,
		systemdClient: systemdClient,
		reader:        rw,
	}

	result := mgr.isSystemShutdown(ctx)
	require.True(t, result)
}

// Helper function to create test utmp data with specific runlevel
func createRunlevelTestData(runlevel byte) []byte {
	data := make([]byte, 384)
	data[0] = 0x01     // Type = RUN_LVL
	data[4] = runlevel // Pid = runlevel character
	return data
}

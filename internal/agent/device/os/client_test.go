package os

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const rhelOSRelease = `NAME="Red Hat Enterprise Linux"
VERSION="9.4 (Plow)"
ID="rhel"
VERSION_ID="9.4"
PRETTY_NAME="Red Hat Enterprise Linux 9.4 (Plow)"
`

const ubuntuOSRelease = `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu
`

func TestPackageModeClientMode(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newPackageModeClient(log.NewPrefixLogger("test"), mockReader)

	require.Equal("package-mode", client.Mode())
}

func TestPackageModeClientStatus(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		setupMocks     func(*fileio.MockReader)
		expectedMode   string
		expectedName   string
		expectedVer    string
		expectError    bool
		expectZeroHost bool
	}{
		{
			name: "When reading RHEL os-release it should populate OS fields with zero-valued BootcHost",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(rhelOSRelease), nil)
			},
			expectedMode:   "package-mode",
			expectedName:   "Red Hat Enterprise Linux",
			expectedVer:    "9.4",
			expectZeroHost: true,
		},
		{
			name: "When reading Ubuntu os-release it should populate OS fields correctly",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(ubuntuOSRelease), nil)
			},
			expectedMode:   "package-mode",
			expectedName:   "Ubuntu",
			expectedVer:    "24.04",
			expectZeroHost: true,
		},
		{
			name: "When os-release read fails it should return error",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return(nil, fmt.Errorf("file not found"))
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReader := fileio.NewMockReader(ctrl)
			tc.setupMocks(mockReader)

			client := newPackageModeClient(log.NewPrefixLogger("test"), mockReader)
			status, err := client.Status(context.Background())

			if tc.expectError {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tc.expectedMode, status.ManagementMode)
			require.Equal(tc.expectedName, status.OSName)
			require.Equal(tc.expectedVer, status.OSVersion)
			if tc.expectZeroHost {
				require.Equal(container.BootcHost{}, status.BootcHost)
			}
		})
	}
}

func TestPackageModeClientSwitch(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newPackageModeClient(log.NewPrefixLogger("test"), mockReader)

	err := client.Switch(context.Background(), "quay.io/test/image:latest")
	require.ErrorIs(err, ErrOSUpdateNotSupported)
}

func TestPackageModeClientApply(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newPackageModeClient(log.NewPrefixLogger("test"), mockReader)

	err := client.Apply(context.Background())
	require.ErrorIs(err, ErrOSUpdateNotSupported)
}

func TestPackageModeClientRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newPackageModeClient(log.NewPrefixLogger("test"), mockReader)

	err := client.Rollback(context.Background())
	require.ErrorIs(err, ErrOSUpdateNotSupported)
}

func TestBootcClientMode(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newBootcClient(log.NewPrefixLogger("test"), nil, mockReader)

	require.Equal("bootc", client.Mode())
}

func TestRpmOSTreeClientMode(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	client := newRpmOSTreeClient(nil, mockReader)

	require.Equal("rpm-ostree", client.Mode())
}

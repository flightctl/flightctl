package os

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestParseOSRelease(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name        string
		setupMocks  func(*fileio.MockReader)
		expected    map[string]string
		expectError bool
	}{
		{
			name: "When parsing valid RHEL os-release it should return correct fields",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`NAME="Red Hat Enterprise Linux"
VERSION="9.4 (Plow)"
ID="rhel"
VERSION_ID="9.4"
PRETTY_NAME="Red Hat Enterprise Linux 9.4 (Plow)"
`), nil)
			},
			expected: map[string]string{
				"NAME":        "Red Hat Enterprise Linux",
				"VERSION":     "9.4 (Plow)",
				"ID":          "rhel",
				"VERSION_ID":  "9.4",
				"PRETTY_NAME": "Red Hat Enterprise Linux 9.4 (Plow)",
			},
		},
		{
			name: "When parsing valid Ubuntu os-release it should return correct fields",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu
`), nil)
			},
			expected: map[string]string{
				"PRETTY_NAME": "Ubuntu 24.04.1 LTS",
				"NAME":        "Ubuntu",
				"VERSION_ID":  "24.04",
				"VERSION":     "24.04.1 LTS (Noble Numbat)",
				"ID":          "ubuntu",
			},
		},
		{
			name: "When os-release has unquoted values it should parse them correctly",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`NAME=Fedora
ID=fedora
VERSION_ID=41
`), nil)
			},
			expected: map[string]string{
				"NAME":       "Fedora",
				"ID":         "fedora",
				"VERSION_ID": "41",
			},
		},
		{
			name: "When os-release is missing NAME and VERSION_ID it should return available fields",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`ID="rhel"
PRETTY_NAME="Red Hat Enterprise Linux"
`), nil)
			},
			expected: map[string]string{
				"ID":          "rhel",
				"PRETTY_NAME": "Red Hat Enterprise Linux",
			},
		},
		{
			name: "When os-release file cannot be read it should return error",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return(nil, fmt.Errorf("file not found"))
			},
			expectError: true,
		},
		{
			name: "When os-release file is empty it should return empty map",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(""), nil)
			},
			expected: map[string]string{},
		},
		{
			name: "When os-release has single-quoted values it should strip them",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`NAME='TestOS'
VERSION_ID='2.0'
ID='testos'
`), nil)
			},
			expected: map[string]string{
				"NAME":       "TestOS",
				"VERSION_ID": "2.0",
				"ID":         "testos",
			},
		},
		{
			name: "When os-release has comments and blank lines it should skip them",
			setupMocks: func(mockReader *fileio.MockReader) {
				mockReader.EXPECT().ReadFile(osReleasePath).Return([]byte(
					`# This is a comment
NAME="TestOS"

# Another comment
VERSION_ID="1.0"
`), nil)
			},
			expected: map[string]string{
				"NAME":       "TestOS",
				"VERSION_ID": "1.0",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReader := fileio.NewMockReader(ctrl)
			tc.setupMocks(mockReader)

			result, err := ParseOSRelease(mockReader)

			if tc.expectError {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tc.expected, result)
		})
	}
}

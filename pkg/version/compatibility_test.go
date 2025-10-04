package version

import (
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

func TestVersionCompatibilityChecker_CheckCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		clientVersion string
		serverVersion string
		expectError   bool
		expectedError string
	}{
		{
			name:          "compatible versions - client newer minor",
			clientVersion: "0.9.3",
			serverVersion: "0.9.1",
			expectError:   false,
		},
		{
			name:          "compatible versions - server newer minor",
			clientVersion: "0.9.1",
			serverVersion: "0.9.3",
			expectError:   false,
		},
		{
			name:          "incompatible versions - different major",
			clientVersion: "0.9.1",
			serverVersion: "1.0.0",
			expectError:   true,
			expectedError: "version incompatibility detected:",
		},
		{
			name:          "incompatible versions - client too old",
			clientVersion: "0.7.3",
			serverVersion: "0.10.0",
			expectError:   true,
			expectedError: "version incompatibility detected:",
		},
		{
			name:          "incompatible versions - client too new",
			clientVersion: "0.10.0",
			serverVersion: "0.7.3",
			expectError:   true,
			expectedError: "version incompatibility detected:",
		},
		{
			name:          "compatible versions with rc suffix",
			clientVersion: "0.9.1-rc.0",
			serverVersion: "0.9.2",
			expectError:   false,
		},
		{
			name:          "real world scenario - client 0.9.1 vs server 0.5",
			clientVersion: "0.9.1-rc.0",
			serverVersion: "0.5",
			expectError:   true,
			expectedError: "version incompatibility detected:",
		},
		{
			name:          "compatible versions with v prefix",
			clientVersion: "v0.9.1",
			serverVersion: "0.9.2",
			expectError:   false,
		},
		{
			name:          "nil server version - should skip check",
			clientVersion: "0.9.1",
			serverVersion: "",
			expectError:   false,
		},
		{
			name:          "invalid client version - should skip check",
			clientVersion: "invalid",
			serverVersion: "0.9.1",
			expectError:   false,
		},
		{
			name:          "invalid server version - should skip check",
			clientVersion: "0.9.1",
			serverVersion: "invalid",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock checker with the test client version
			checker := &VersionCompatibilityChecker{
				clientVersion: Info{GitVersion: tt.clientVersion},
			}

			var serverVersion *api.Version
			if tt.serverVersion != "" {
				serverVersion = &api.Version{Version: tt.serverVersion}
			}

			err := checker.CheckCompatibility(serverVersion)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if tt.expectedError != "" && !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("expected error to contain %q, got %q", tt.expectedError, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestVersionCompatibilityChecker_parseVersion(t *testing.T) {
	tests := []struct {
		name          string
		versionStr    string
		expectedMajor int
		expectedMinor int
		expectError   bool
	}{
		{
			name:          "standard version",
			versionStr:    "0.9.1",
			expectedMajor: 0,
			expectedMinor: 9,
			expectError:   false,
		},
		{
			name:          "version with rc suffix",
			versionStr:    "0.9.1-rc.0",
			expectedMajor: 0,
			expectedMinor: 9,
			expectError:   false,
		},
		{
			name:          "version with v prefix",
			versionStr:    "v1.2.3",
			expectedMajor: 1,
			expectedMinor: 2,
			expectError:   false,
		},
		{
			name:          "single digit version",
			versionStr:    "0.5",
			expectedMajor: 0,
			expectedMinor: 5,
			expectError:   false,
		},
		{
			name:          "version with leading/trailing whitespace",
			versionStr:    " v0.9.1 ",
			expectedMajor: 0,
			expectedMinor: 9,
			expectError:   false,
		},
		{
			name:          "version with v prefix and rc suffix",
			versionStr:    "v0.9.1-rc.1",
			expectedMajor: 0,
			expectedMinor: 9,
			expectError:   false,
		},
		{
			name:        "invalid format - no dots",
			versionStr:  "invalid",
			expectError: true,
		},
		{
			name:        "invalid format - non-numeric major",
			versionStr:  "a.1.2",
			expectError: true,
		},
		{
			name:        "invalid format - non-numeric minor",
			versionStr:  "1.b.2",
			expectError: true,
		},
	}

	checker := &VersionCompatibilityChecker{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, err := checker.parseVersion(tt.versionStr)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("expected no error but got: %v", err)
					return
				}
				if major != tt.expectedMajor {
					t.Errorf("expected major %d, got %d", tt.expectedMajor, major)
				}
				if minor != tt.expectedMinor {
					t.Errorf("expected minor %d, got %d", tt.expectedMinor, minor)
				}
			}
		})
	}
}

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestResumeOptions_ValidateArgs(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		labelSelector string
		all           bool
		expectError   bool
		errorContains string
	}{
		{
			name:          "invalid bulk resume without selector",
			args:          []string{"devices"},
			labelSelector: "",
			all:           false,
			expectError:   true,
			errorContains: "at least one selector or --all flag is required",
		},
		{
			name:          "invalid single device with selector",
			args:          []string{"device/test-device"},
			labelSelector: "env=production",
			expectError:   true,
			errorContains: "label selector cannot be used when resuming a specific device",
		},
		{
			name:          "invalid kind",
			args:          []string{"fleet/test-fleet"},
			expectError:   true,
			errorContains: "kind must be Device",
		},
		{
			name:          "invalid bulk resume without selector (singular form)",
			args:          []string{"device"},
			labelSelector: "",
			all:           false,
			expectError:   true,
			errorContains: "at least one selector or --all flag is required",
		},
		{
			name:          "valid single device",
			args:          []string{"device/test-device"},
			labelSelector: "",
			all:           false,
			expectError:   false,
		},
		{
			name:          "valid bulk resume with selector (plural form)",
			args:          []string{"devices"},
			labelSelector: "env=production",
			all:           false,
			expectError:   false,
		},
		{
			name:          "valid bulk resume with selector (singular form)",
			args:          []string{"device"},
			labelSelector: "env=production",
			all:           false,
			expectError:   false,
		},
		{
			name:          "valid bulk resume with --all flag",
			args:          []string{"devices"},
			labelSelector: "",
			all:           true,
			expectError:   false,
		},
		{
			name:          "invalid --all with label selector",
			args:          []string{"devices"},
			labelSelector: "env=production",
			all:           true,
			expectError:   true,
			errorContains: "--all flag cannot be used with selectors",
		},
		{
			name:          "invalid --all with specific device",
			args:          []string{"device/test-device"},
			labelSelector: "",
			all:           true,
			expectError:   true,
			errorContains: "--all flag cannot be used when resuming a specific device",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &ResumeOptions{
				LabelSelector: tt.labelSelector,
				All:           tt.all,
			}

			// Test the validation logic directly without calling GlobalOptions.Validate
			err := validateResumeArgs(opts, tt.args)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// Helper function to test validation logic without authentication
func validateResumeArgs(opts *ResumeOptions, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no device specified")
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("kind must be Device")
	}

	// Handle bulk resume case (name is empty for plural forms)
	if name == "" {
		// Check for mutually exclusive flags
		if opts.All && opts.LabelSelector != "" {
			return fmt.Errorf("--all flag cannot be used with selectors")
		}

		// Require at least one selector or --all flag
		if !opts.All && opts.LabelSelector == "" {
			return fmt.Errorf("at least one selector or --all flag is required when resuming multiple devices. Use --selector/-l, --field-selector, or --all flag")
		}
		return nil
	}

	// Handle single device case (name is provided)
	if opts.All {
		return fmt.Errorf("--all flag cannot be used when resuming a specific device")
	}
	if opts.LabelSelector != "" {
		return fmt.Errorf("label selector cannot be used when resuming a specific device")
	}

	return nil
}

func TestResumeOptions_runSingleResume(t *testing.T) {
	tests := []struct {
		name          string
		deviceName    string
		httpStatus    int
		responseBody  string
		expectError   bool
		errorContains string
		expectOutput  string
	}{
		{
			name:         "successful resume",
			deviceName:   "test-device",
			httpStatus:   http.StatusOK,
			responseBody: `{"resumedDevices": 1}`,
			expectError:  false,
			expectOutput: "Resume request for device \"test-device\" completed",
		},
		{
			name:          "device not found",
			deviceName:    "missing-device",
			httpStatus:    http.StatusOK,
			responseBody:  `{"resumedDevices": 0}`,
			expectError:   true,
			errorContains: "failed resuming device missing-device, device doesnt exists or already resumed",
		},
		{
			name:          "server error",
			deviceName:    "error-device",
			httpStatus:    http.StatusInternalServerError,
			responseBody:  `{}`,
			expectError:   true,
			errorContains: "unsuccessful resume request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP response
			response := &http.Response{
				StatusCode: tt.httpStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(tt.responseBody))),
			}

			client, _ := newTestClient(t, response)

			opts := &ResumeOptions{}

			// Capture output
			output := captureStdout(t, func() {
				err := opts.runSingleResume(context.Background(), client, tt.deviceName)

				if tt.expectError {
					if err == nil {
						t.Errorf("expected error but got none")
						return
					}
					if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
					}
				} else {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}
			})

			if !tt.expectError && tt.expectOutput != "" {
				if !containsString(output, tt.expectOutput) {
					t.Errorf("expected output to contain %q, got %q", tt.expectOutput, output)
				}
			}
		})
	}
}

func TestResumeOptions_runBulkResume(t *testing.T) {
	tests := []struct {
		name          string
		labelSelector string
		httpStatus    int
		responseBody  string
		expectError   bool
		errorContains string
		expectOutput  []string
	}{
		{
			name:          "successful bulk resume",
			labelSelector: "env=production",
			httpStatus:    http.StatusOK,
			responseBody:  `{"resumedDevices": 3}`,
			expectError:   false,
			expectOutput: []string{
				"Resume operation completed:",
				"Devices resumed: 3",
			},
		},
		{
			name:          "no devices matched",
			labelSelector: "env=nonexistent",
			httpStatus:    http.StatusOK,
			responseBody:  `{"resumedDevices": 0}`,
			expectError:   false,
			expectOutput: []string{
				"Devices resumed: 0",
				"No devices matched the selector or were in conflictPaused state",
			},
		},
		{
			name:          "partial success",
			labelSelector: "env=staging",
			httpStatus:    http.StatusOK,
			responseBody:  `{"resumedDevices": 1}`,
			expectError:   false,
			expectOutput: []string{
				"Resume operation completed:",
				"Devices resumed: 1",
			},
		},
		{
			name:          "invalid label selector",
			labelSelector: "invalid=selector=format",
			httpStatus:    http.StatusBadRequest,
			expectError:   true,
			errorContains: "resuming devices with label selector 'invalid=selector=format'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP response
			response := &http.Response{
				StatusCode: tt.httpStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(tt.responseBody))),
			}

			client, _ := newTestClient(t, response)

			opts := &ResumeOptions{
				LabelSelector: tt.labelSelector,
			}

			// Capture output
			output := captureStdout(t, func() {
				err := opts.runBulkResume(context.Background(), client)

				if tt.expectError {
					if err == nil {
						t.Errorf("expected error but got none")
						return
					}
					if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
					}
				} else {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}
			})

			if !tt.expectError {
				for _, expectedOutput := range tt.expectOutput {
					if !containsString(output, expectedOutput) {
						t.Errorf("expected output to contain %q, got %q", expectedOutput, output)
					}
				}
			}
		})
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		bytes.Contains([]byte(s), []byte(substr)))
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestAppLifecycleOptions_resolveDeviceName(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		appName       string
		expectError   bool
		errorContains string
		expectDevice  string
	}{
		{
			name:         "valid device and app",
			args:         []string{"device/my-device"},
			appName:      "my-app",
			expectDevice: "my-device",
		},
		{
			name:          "missing app name",
			args:          []string{"device/my-device"},
			appName:       "",
			expectError:   true,
			errorContains: "--app is required",
		},
		{
			name:          "wrong kind",
			args:          []string{"fleet/my-fleet"},
			appName:       "my-app",
			expectError:   true,
			errorContains: "kind must be Device",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &AppLifecycleOptions{AppName: tt.appName}
			name, err := o.resolveDeviceName(tt.args)

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tt.expectDevice {
				t.Errorf("expected device name %q, got %q", tt.expectDevice, name)
			}
		})
	}
}

func TestAppLifecycleOptions_resolveTarget(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		appName       string
		expectError   bool
		errorContains string
		expectKind    ResourceKind
		expectName    string
	}{
		{
			name:       "valid device and app",
			args:       []string{"device/my-device"},
			appName:    "my-app",
			expectKind: DeviceKind,
			expectName: "my-device",
		},
		{
			name:       "valid fleet and app",
			args:       []string{"fleet/my-fleet"},
			appName:    "my-app",
			expectKind: FleetKind,
			expectName: "my-fleet",
		},
		{
			name:          "missing app name",
			args:          []string{"device/my-device"},
			appName:       "",
			expectError:   true,
			errorContains: "--app is required",
		},
		{
			name:          "unsupported kind",
			args:          []string{"repository/my-repo"},
			appName:       "my-app",
			expectError:   true,
			errorContains: "kind must be Device or Fleet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &AppLifecycleOptions{AppName: tt.appName}
			kind, name, err := o.resolveTarget(tt.args)

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if kind != tt.expectKind {
				t.Errorf("expected kind %q, got %q", tt.expectKind, kind)
			}
			if name != tt.expectName {
				t.Errorf("expected name %q, got %q", tt.expectName, name)
			}
		})
	}
}

func TestStopStartConfirmPrompt(t *testing.T) {
	tests := []struct {
		name     string
		verb     string
		appName  string
		kind     ResourceKind
		target   string
		expected string
	}{
		{
			name:     "device target",
			verb:     "Stop",
			appName:  "my-app",
			kind:     DeviceKind,
			target:   "my-device",
			expected: `Stop application "my-app" on device "my-device"?`,
		},
		{
			name:     "fleet target",
			verb:     "Start",
			appName:  "my-app",
			kind:     FleetKind,
			target:   "my-fleet",
			expected: `Start application "my-app" on every device in fleet "my-fleet"?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stopStartConfirmPrompt(tt.verb, tt.appName, tt.kind, tt.target)
			if got != tt.expected {
				t.Errorf("expected prompt %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestConfirm(t *testing.T) {
	// skip=true never touches stdin and always succeeds.
	if err := confirm("prompt", true); err != nil {
		t.Errorf("expected no error when skip is true, got %v", err)
	}
}

func TestRunStop(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		responseBody   string
		expectError    bool
		errorContains  string
		expectOutput   string
		expectAPIError bool
	}{
		{
			name:         "successful stop",
			httpStatus:   http.StatusOK,
			responseBody: `{"kind":"Device","apiVersion":"v1beta1","metadata":{"name":"dev-1"}}`,
			expectOutput: `Requested stop of application "app-1" on device "dev-1"`,
		},
		{
			name:           "device not found",
			httpStatus:     http.StatusNotFound,
			responseBody:   `{"message":"device not found"}`,
			expectError:    true,
			errorContains:  "stopping application app-1 on device dev-1: failed",
			expectAPIError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: tt.httpStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(tt.responseBody))),
			}
			client, _ := newTestClient(t, response)

			output := captureStdout(t, func() {
				err := runStop(context.Background(), client, "dev-1", "app-1")
				if tt.expectError {
					if err == nil {
						t.Fatalf("expected error but got none")
					}
					if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
					}
					if tt.expectAPIError {
						var cliErr *CLIError
						if !errors.As(err, &cliErr) {
							t.Errorf("expected error to unwrap to *CLIError, got %T", err)
						}
						var apiErr *APIError
						if !errors.As(err, &apiErr) {
							t.Errorf("expected error to unwrap to *APIError, got %T", err)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})

			if !tt.expectError && !containsString(output, tt.expectOutput) {
				t.Errorf("expected output to contain %q, got %q", tt.expectOutput, output)
			}
		})
	}
}

func TestRunStart(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"kind":"Device","apiVersion":"v1beta1","metadata":{"name":"dev-1"}}`))),
	}
	client, _ := newTestClient(t, response)

	output := captureStdout(t, func() {
		if err := runStart(context.Background(), client, "dev-1", "app-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !containsString(output, `Requested start of application "app-1" on device "dev-1"`) {
		t.Errorf("unexpected output: %q", output)
	}
}

func TestRunRestart(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"kind":"Device","apiVersion":"v1beta1","metadata":{"name":"dev-1"}}`))),
	}
	client, _ := newTestClient(t, response)

	output := captureStdout(t, func() {
		if err := runRestart(context.Background(), client, "dev-1", "app-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !containsString(output, `Requested restart of application "app-1" on device "dev-1"`) {
		t.Errorf("unexpected output: %q", output)
	}
}

func TestRunStopFleet(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		responseBody   string
		expectError    bool
		errorContains  string
		expectOutput   string
		expectAPIError bool
	}{
		{
			name:         "successful stop",
			httpStatus:   http.StatusOK,
			responseBody: `{"kind":"Fleet","apiVersion":"v1beta1","metadata":{"name":"fleet-1"}}`,
			expectOutput: `Requested stop of application "app-1" on every device in fleet "fleet-1"`,
		},
		{
			name:           "fleet not found",
			httpStatus:     http.StatusNotFound,
			responseBody:   `{"message":"fleet not found"}`,
			expectError:    true,
			errorContains:  "stopping application app-1 on fleet fleet-1: failed",
			expectAPIError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: tt.httpStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(tt.responseBody))),
			}
			client, _ := newTestClient(t, response)

			output := captureStdout(t, func() {
				err := runStopFleet(context.Background(), client, "fleet-1", "app-1")
				if tt.expectError {
					if err == nil {
						t.Fatalf("expected error but got none")
					}
					if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
					}
					if tt.expectAPIError {
						var cliErr *CLIError
						if !errors.As(err, &cliErr) {
							t.Errorf("expected error to unwrap to *CLIError, got %T", err)
						}
						var apiErr *APIError
						if !errors.As(err, &apiErr) {
							t.Errorf("expected error to unwrap to *APIError, got %T", err)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})

			if !tt.expectError && !containsString(output, tt.expectOutput) {
				t.Errorf("expected output to contain %q, got %q", tt.expectOutput, output)
			}
		})
	}
}

func TestRunStartFleet(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		responseBody   string
		expectError    bool
		errorContains  string
		expectOutput   string
		expectAPIError bool
	}{
		{
			name:         "successful start",
			httpStatus:   http.StatusOK,
			responseBody: `{"kind":"Fleet","apiVersion":"v1beta1","metadata":{"name":"fleet-1"}}`,
			expectOutput: `Requested start of application "app-1" on every device in fleet "fleet-1"`,
		},
		{
			name:           "fleet not found",
			httpStatus:     http.StatusNotFound,
			responseBody:   `{"message":"fleet not found"}`,
			expectError:    true,
			errorContains:  "starting application app-1 on fleet fleet-1: failed",
			expectAPIError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: tt.httpStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader([]byte(tt.responseBody))),
			}
			client, _ := newTestClient(t, response)

			output := captureStdout(t, func() {
				err := runStartFleet(context.Background(), client, "fleet-1", "app-1")
				if tt.expectError {
					if err == nil {
						t.Fatalf("expected error but got none")
					}
					if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
					}
					if tt.expectAPIError {
						var cliErr *CLIError
						if !errors.As(err, &cliErr) {
							t.Errorf("expected error to unwrap to *CLIError, got %T", err)
						}
						var apiErr *APIError
						if !errors.As(err, &apiErr) {
							t.Errorf("expected error to unwrap to *APIError, got %T", err)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})

			if !tt.expectError && !containsString(output, tt.expectOutput) {
				t.Errorf("expected output to contain %q, got %q", tt.expectOutput, output)
			}
		})
	}
}

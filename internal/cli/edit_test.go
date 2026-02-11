package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"sigs.k8s.io/yaml"
)

func TestEditOptions_Validate(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		fleetName     string
		expectError   bool
		errorContains string
		errorIs       error
	}{
		{
			name:        "valid TYPE/NAME format",
			args:        []string{"device/test-device"},
			expectError: false,
		},
		{
			name:        "valid TYPE NAME format",
			args:        []string{"device", "test-device"},
			expectError: false,
		},
		{
			name:          "invalid - no resource name with TYPE format",
			args:          []string{"device"},
			expectError:   true,
			errorContains: "you must specify a resource name to edit",
		},
		{
			name:          "invalid - mixed format",
			args:          []string{"device/test-device", "extra-arg"},
			expectError:   true,
			errorContains: "cannot mix TYPE/NAME syntax with additional resource names",
		},
		{
			name:          "invalid resource kind",
			args:          []string{"invalidkind/test"},
			expectError:   true,
			errorContains: "invalid resource kind: invalidkind",
		},
		{
			name:        "invalid - cannot edit events",
			args:        []string{"event/test-event"},
			expectError: true,
			errorIs:     errEditNotAllowed{EventKind},
		},
		{
			name:        "invalid - cannot edit organizations",
			args:        []string{"organization/test-org"},
			expectError: true,
			errorIs:     errEditNotAllowed{OrganizationKind},
		},
		{
			name:        "invalid - templateversion without fleetname",
			args:        []string{"templateversion/test-tv"},
			expectError: true,
			errorIs:     errEditNotAllowed{TemplateVersionKind},
		},
		{
			name:        "invalid - templateversion with fleetname",
			args:        []string{"templateversion/test-tv"},
			fleetName:   "test-fleet",
			expectError: true,
			errorIs:     errEditNotAllowed{TemplateVersionKind},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultEditOptions()
			opts.FleetName = tc.fleetName

			// Mock the config file path to avoid login requirement
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "client.yaml")
			writeTestConfig(t, configPath, "test-org")
			opts.ConfigFilePath = configPath

			err := opts.Validate(tc.args)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
				}
				if tc.errorIs != nil && !errors.Is(err, tc.errorIs) {
					t.Errorf("expected error to be %v, got %v", tc.errorIs, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestEditOptions_Complete(t *testing.T) {
	tests := []struct {
		name           string
		editorFlag     string
		kubeEditor     string
		editor         string
		expectedEditor string
	}{
		{
			name:           "uses editor flag when provided",
			editorFlag:     "nano",
			kubeEditor:     "vim",
			editor:         "emacs",
			expectedEditor: "nano",
		},
		{
			name:           "uses FLIGHTCTL_EDITOR when no flag",
			editorFlag:     "",
			kubeEditor:     "vim",
			editor:         "emacs",
			expectedEditor: "vim",
		},
		{
			name:           "uses EDITOR when no flag or FLIGHTCTL_EDITOR",
			editorFlag:     "",
			kubeEditor:     "",
			editor:         "emacs",
			expectedEditor: "emacs",
		},
		{
			name:           "defaults to vi when no env vars",
			editorFlag:     "",
			kubeEditor:     "",
			editor:         "",
			expectedEditor: "vi",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set up environment
			t.Setenv("FLIGHTCTL_EDITOR", tc.kubeEditor)
			t.Setenv("EDITOR", tc.editor)
			t.Setenv("VISUAL", tc.editor)

			opts := DefaultEditOptions()
			opts.Editor = tc.editorFlag

			err := opts.Complete(nil, []string{"device/test"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if opts.Editor != tc.expectedEditor {
				t.Errorf("expected editor %q, got %q", tc.expectedEditor, opts.Editor)
			}
		})
	}
}

func TestEditOptions_resourceToYAML(t *testing.T) {
	// Create a mock device response
	device := &api.Device{
		Metadata: api.ObjectMeta{
			Name: stringPtr("test-device"),
		},
		Spec: &api.DeviceSpec{},
	}

	response := &apiclient.GetDeviceResponse{
		JSON200: device,
		HTTPResponse: &http.Response{
			StatusCode: 200,
		},
	}

	opts := DefaultEditOptions()
	yamlBytes, err := opts.resourceToFormat(response, opts.Output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it's valid YAML and contains expected content
	var result map[string]interface{}
	err = yaml.Unmarshal(yamlBytes, &result)
	if err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	metadata, ok := result["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata field in YAML")
	}
	if metadata["name"] != "test-device" {
		t.Errorf("expected name %q, got %q", "test-device", metadata["name"])
	}
}

func TestEditOptions_applyChanges(t *testing.T) {
	tests := []struct {
		name          string
		kind          ResourceKind
		resourceName  string
		yamlContent   string
		setupClient   func(t *testing.T) *apiclient.ClientWithResponses
		expectError   bool
		errorContains string
	}{
		{
			name:         "successful device update",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
apiVersion: flightctl.io/v1beta1
kind: Device
metadata:
  name: test-device
spec: {}
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				response := &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("")),
				}
				client, _ := newTestClient(t, response)
				return client
			},
			expectError: false,
		},
		{
			name:         "invalid YAML",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent:  "invalid: yaml: content:",
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				client, _ := newTestClient(t)
				return client
			},
			expectError:   true,
			errorContains: "parsing edited YAML",
		},
		{
			name:         "missing kind field",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
metadata:
  name: test-device
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				client, _ := newTestClient(t)
				return client
			},
			expectError:   true,
			errorContains: "missing 'kind' field",
		},
		{
			name:         "kind mismatch",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
kind: Fleet
metadata:
  name: test-device
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				client, _ := newTestClient(t)
				return client
			},
			expectError:   true,
			errorContains: "cannot change resource kind from device to Fleet",
		},
		{
			name:         "name mismatch",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
kind: Device
metadata:
  name: different-name
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				client, _ := newTestClient(t)
				return client
			},
			expectError:   true,
			errorContains: "cannot change resource name from test-device to different-name",
		},
		{
			name:         "server error",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
apiVersion: flightctl.io/v1beta1
kind: Device
metadata:
  name: test-device
spec:
  osImage: "test-image"
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				response := &http.Response{
					StatusCode: 500,
					Status:     "500 Internal Server Error",
					Body:       io.NopCloser(strings.NewReader("")),
				}
				client, _ := newTestClient(t, response)
				return client
			},
			expectError:   true,
			errorContains: "server returned status: 500 Internal Server Error",
		},
		{
			name:         "resourceVersion conflict - 409 Conflict",
			kind:         DeviceKind,
			resourceName: "test-device",
			yamlContent: `
apiVersion: flightctl.io/v1beta1
kind: Device
metadata:
  name: test-device
  resourceVersion: "1"
spec:
  osImage: "updated-image"
`,
			setupClient: func(t *testing.T) *apiclient.ClientWithResponses {
				response := &http.Response{
					StatusCode: 409,
					Status:     "409 Conflict",
					Body:       io.NopCloser(strings.NewReader(`{"error": "resourceVersion conflict"}`)),
				}
				client, _ := newTestClient(t, response)
				return client
			},
			expectError:   true,
			errorContains: "server returned status: 409 Conflict",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultEditOptions()
			client := tc.setupClient(t)

			// Create a mock originalResource for the test
			resourceName := tc.resourceName
			resourceVersion := "1"
			// Parse the YAML content to extract the kind and apiVersion
			var yamlResource map[string]interface{}
			if err := yaml.Unmarshal([]byte(tc.yamlContent), &yamlResource); err == nil {
				// Use the kind and apiVersion from the YAML content
				kind := "Device" // default
				var apiVersion string
				if k, ok := yamlResource["kind"].(string); ok {
					kind = k
				}
				if av, ok := yamlResource["apiVersion"].(string); ok {
					apiVersion = av
				}

				// Create a mock originalResource that matches the YAML content structure
				originalResource := &apiclient.GetDeviceResponse{
					JSON200: &api.Device{
						ApiVersion: apiVersion,
						Kind:       kind,
						Metadata: api.ObjectMeta{
							Name:            &resourceName,
							ResourceVersion: &resourceVersion,
						},
						Spec: &api.DeviceSpec{},
					},
				}

				err := opts.applyChanges(context.Background(), client, []byte(tc.yamlContent), tc.kind, tc.resourceName, originalResource)

				if tc.expectError {
					if err == nil {
						t.Errorf("expected error but got none")
					}
					if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
					}
				} else {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}
			} else {
				// If YAML parsing fails, use defaults
				originalResource := &apiclient.GetDeviceResponse{
					JSON200: &api.Device{
						ApiVersion: "flightctl.io/v1beta1",
						Kind:       "Device",
						Metadata: api.ObjectMeta{
							Name:            &resourceName,
							ResourceVersion: &resourceVersion,
						},
						Spec: &api.DeviceSpec{},
					},
				}

				err := opts.applyChanges(context.Background(), client, []byte(tc.yamlContent), tc.kind, tc.resourceName, originalResource)

				if tc.expectError {
					if err == nil {
						t.Errorf("expected error but got none")
					}
					if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
						t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
					}
				} else {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}
			}
		})
	}
}

func TestEditOptions_saveToTempFile(t *testing.T) {
	opts := DefaultEditOptions()
	content := []byte("test content")
	kind := DeviceKind
	name := "test-device"

	tempFile, err := opts.saveToTempFile(content, kind, name)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(tempFile)

	// Verify file exists and has correct content
	savedContent, err := os.ReadFile(tempFile)
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}
	if !bytes.Equal(content, savedContent) {
		t.Errorf("expected content %q, got %q", string(content), string(savedContent))
	}

	// Verify filename pattern
	if !strings.Contains(tempFile, "flightctl-edit-failed-device-test-device") {
		t.Errorf("expected filename to contain pattern, got %q", tempFile)
	}
	if !strings.Contains(tempFile, ".yaml") {
		t.Errorf("expected filename to contain .yaml, got %q", tempFile)
	}
}

func TestEditOptions_Run_ArgumentHandling(t *testing.T) {
	// Test how the Run method handles different argument formats
	tests := []struct {
		name         string
		args         []string
		expectedKind ResourceKind
		expectedName string
	}{
		{
			name:         "TYPE/NAME format",
			args:         []string{"device/test-device"},
			expectedKind: DeviceKind,
			expectedName: "test-device",
		},
		{
			name:         "TYPE NAME format",
			args:         []string{"device", "test-device"},
			expectedKind: DeviceKind,
			expectedName: "test-device",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultEditOptions()

			// Mock the config file path to avoid login requirement
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "client.yaml")
			writeTestConfig(t, configPath, "test-org")
			opts.ConfigFilePath = configPath

			// We can't easily test the full Run method without mocking the client,
			// but we can test the argument parsing logic by checking validation
			err := opts.Validate(tc.args)
			if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}

			// Test that parseAndValidateKindName works correctly
			kind, name, err := parseAndValidateKindName(tc.args[0])
			if err != nil {
				t.Fatalf("unexpected parsing error: %v", err)
			}

			// Handle TYPE NAME format
			if len(name) == 0 && len(tc.args) >= 2 {
				name = tc.args[1]
			}

			if kind != tc.expectedKind {
				t.Errorf("expected kind %q, got %q", tc.expectedKind, kind)
			}
			if name != tc.expectedName {
				t.Errorf("expected name %q, got %q", tc.expectedName, name)
			}
		})
	}
}

func TestEditOptions_calculateJSONPatch(t *testing.T) {
	tests := []struct {
		name                string
		originalResource    map[string]interface{}
		editedResource      map[string]interface{}
		expectError         bool
		errorContains       string
		expectedPatchOps    int
		shouldIncludeTestOp bool
	}{
		{
			name: "successful patch with resourceVersion test",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "old-image",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "new-image",
				},
			},
			expectError:         false,
			expectedPatchOps:    2, // 1 for spec change + 1 for resourceVersion test
			shouldIncludeTestOp: true,
		},
		{
			name: "no changes - should return only resourceVersion test",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "same-image",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "same-image",
				},
			},
			expectError:         false,
			expectedPatchOps:    1, // Only resourceVersion test when no changes
			shouldIncludeTestOp: true,
		},
		{
			name: "protected field change - should be filtered and return only resourceVersion test",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Fleet", // This should be filtered out
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
			},
			expectError:         false,
			expectedPatchOps:    1, // Only resourceVersion test when only protected fields changed
			shouldIncludeTestOp: true,
		},
		{
			name: "user changed resourceVersion - should include test and fail",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "old-image",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "2", // User changed this
				},
				"spec": map[string]interface{}{
					"osImage": "new-image",
				},
			},
			expectError:         false, // Patch generation succeeds, but server will reject due to test op
			expectedPatchOps:    3,     // 1 for spec change + 1 for resourceVersion change + 1 for resourceVersion test
			shouldIncludeTestOp: true,
		},
		{
			name: "user removed resourceVersion - should not include test operation",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "old-image",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name": "test-device",
					// resourceVersion removed by user
				},
				"spec": map[string]interface{}{
					"osImage": "new-image",
				},
			},
			expectError:         false,
			expectedPatchOps:    2,     // 1 for spec change + 1 for resourceVersion removal
			shouldIncludeTestOp: false, // No test op when user removes resourceVersion
		},
		{
			name: "user removed entire metadata - should not include test operation",
			originalResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name":            "test-device",
					"resourceVersion": "1",
				},
				"spec": map[string]interface{}{
					"osImage": "old-image",
				},
			},
			editedResource: map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				// metadata removed entirely by user
				"spec": map[string]interface{}{
					"osImage": "new-image",
				},
			},
			expectError:         false,
			expectedPatchOps:    2,     // 1 for spec change + 1 for metadata removal
			shouldIncludeTestOp: false, // No test op when user removes metadata
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultEditOptions()

			patchJSON, err := opts.calculateJSONPatch(tc.originalResource, tc.editedResource)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Parse the patch to verify its contents
			var patch []map[string]interface{}
			if err := json.Unmarshal(patchJSON, &patch); err != nil {
				t.Fatalf("failed to unmarshal patch: %v", err)
			}

			if len(patch) != tc.expectedPatchOps {
				t.Errorf("expected %d patch operations, got %d", tc.expectedPatchOps, len(patch))
			}

			// Check if resourceVersion test operation is included
			hasResourceVersionTest := false
			for _, op := range patch {
				if op["op"] == "test" && op["path"] == "/metadata/resourceVersion" {
					hasResourceVersionTest = true
					break
				}
			}

			if tc.shouldIncludeTestOp && !hasResourceVersionTest {
				t.Errorf("expected resourceVersion test operation to be included")
			}
			if !tc.shouldIncludeTestOp && hasResourceVersionTest {
				t.Errorf("unexpected resourceVersion test operation found")
			}
		})
	}
}

func TestEditCommand_CobraValidation(t *testing.T) {
	// Test cobra's built-in argument validation
	cmd := NewCmdEdit()

	// Test too few arguments
	cmd.SetArgs([]string{})
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Errorf("expected error for too few arguments")
	}

	// Test too many arguments
	cmd.SetArgs([]string{"device", "name1", "name2"})
	err = cmd.Args(cmd, []string{"device", "name1", "name2"})
	if err == nil {
		t.Errorf("expected error for too many arguments")
	}

	// Test valid argument counts
	cmd.SetArgs([]string{"device/test"})
	err = cmd.Args(cmd, []string{"device/test"})
	if err != nil {
		t.Errorf("unexpected error for valid single argument: %v", err)
	}

	cmd.SetArgs([]string{"device", "test"})
	err = cmd.Args(cmd, []string{"device", "test"})
	if err != nil {
		t.Errorf("unexpected error for valid two arguments: %v", err)
	}
}

// Helper functions

// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

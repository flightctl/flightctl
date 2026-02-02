package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/cli/display"
	"github.com/flightctl/flightctl/internal/client"
)

// fakeHTTPClient is a minimal HTTP client stub that returns the supplied
// responses sequentially.
type fakeHTTPClient struct {
	responses []*http.Response
	callCount int
}

func (f *fakeHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	if f.callCount >= len(f.responses) {
		return nil, fmt.Errorf("no more responses available, already made %d calls", f.callCount)
	}
	resp := f.responses[f.callCount]
	f.callCount++
	return resp, nil
}

// newTestClient wires the fake client into a ClientWithResponses instance and
// returns both so the caller can assert on the number of calls.
func newTestClient(t *testing.T, responses ...*http.Response) (*apiclient.ClientWithResponses, *fakeHTTPClient) {
	t.Helper()

	fake := &fakeHTTPClient{responses: responses}
	c, err := apiclient.NewClientWithResponses("http://example.com", apiclient.WithHTTPClient(fake))
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	return c, fake
}

// makeDeviceListPage builds an *http.Response that contains a single page of a
// DeviceList with the desired metadata values.
func makeDeviceListPage(t *testing.T, numItems int, cont *string, remaining *int64) *http.Response {
	t.Helper()

	items := make([]api.Device, numItems)
	for i := 0; i < numItems; i++ {
		name := fmt.Sprintf("dev-%d", i)
		items[i] = api.Device{
			ApiVersion: "v1",
			Kind:       api.DeviceKind,
			Metadata:   api.ObjectMeta{Name: &name},
		}
	}

	body, err := json.Marshal(api.DeviceList{
		ApiVersion: "v1",
		Kind:       api.DeviceListKind,
		Items:      items,
		Metadata: api.ListMeta{
			Continue:           cont,
			RemainingItemCount: remaining,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal device list: %v", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// captureStdout redirects Stdout while fn executes and returns what was
// written. This avoids polluting test output and makes assertions easy.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}

	// Ensure we always restore stdout, even if there's a panic
	defer func() {
		os.Stdout = original
	}()

	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.Copy(&buf, r)
		if err != nil {
			t.Errorf("failed to copy from pipe: %v", err)
		}
	}()

	fn()

	w.Close()
	<-done
	return buf.String()
}

func TestHandleListBatching(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		limit          int32
		responsesItems []int // number of items per server page
		expectCalls    int
	}{
		{
			name:           "table_unlimited",
			output:         "", // default table format
			limit:          0,
			responsesItems: []int{maxRequestLimit, 42},
			expectCalls:    2,
		},
		{
			name:           "table_client_side_limit",
			output:         "",
			limit:          1100,
			responsesItems: []int{maxRequestLimit, 42},
			expectCalls:    2,
		},
		{
			name:           "json_output_single_call",
			output:         "json",
			limit:          0,
			responsesItems: []int{42, 42}, // client should stop after first page - running in batches only in table mode
			expectCalls:    1,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// Build fake HTTP responses according to the desired pagination
			var responses []*http.Response
			for i, numItems := range testCase.responsesItems {
				var (
					continueToken *string
					remaining     *int64
				)
				if i < len(testCase.responsesItems)-1 {
					token := fmt.Sprintf("token-%d", i)
					continueToken = &token
					remainingCount := int64(testCase.responsesItems[i+1])
					remaining = &remainingCount
				}
				responses = append(responses, makeDeviceListPage(t, numItems, continueToken, remaining))
			}

			apiClient, fakeClient := newTestClient(t, responses...)
			c := client.NewTestClient(apiClient)

			opts := DefaultGetOptions()
			opts.Output = testCase.output
			opts.Limit = testCase.limit

			ctx := context.Background()
			fetcher := func() (interface{}, error) {
				response, err := opts.getResourceList(ctx, c, DeviceKind)
				if err != nil {
					return nil, err
				}
				if err := validateResponse(response); err != nil {
					return nil, err
				}
				return response, nil
			}

			// Capture stdout to avoid polluting test output
			_ = captureStdout(t, func() {
				err := opts.handleList(ctx, display.NewFormatter(display.OutputFormat(opts.Output)), DeviceKind, fetcher)
				if err != nil {
					t.Fatalf("handleList returned unexpected error: %v", err)
				}
			})

			if fakeClient.callCount != testCase.expectCalls {
				t.Errorf("expected %d HTTP calls, got %d", testCase.expectCalls, fakeClient.callCount)
			}
		})
	}
}

func TestParseAndValidateKindNameFromArgs(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		expectedKind  ResourceKind
		expectedNames []string
		expectError   bool
		errorContains string
	}{
		// TYPE/NAME format tests
		{
			name:          "single_device_slash_format",
			args:          []string{"device/test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "short_form_slash_format",
			args:          []string{"dev/test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "plural_slash_format",
			args:          []string{"devices/test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "enrollment_request_slash_format",
			args:          []string{"er/test1"},
			expectedKind:  EnrollmentRequestKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "slash_format_no_name",
			args:          []string{"device/"},
			expectedKind:  DeviceKind,
			expectedNames: []string{},
			expectError:   true,
			errorContains: "resource name cannot be empty when using TYPE/NAME format",
		},
		{
			name:          "slash_format_with_extra_args",
			args:          []string{"device/test1", "extra"},
			expectError:   true,
			errorContains: "cannot mix TYPE/NAME syntax with additional arguments",
		},

		// TYPE NAME format tests
		{
			name:          "single_device_space_format",
			args:          []string{"device", "test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "short_form_space_format",
			args:          []string{"dev", "test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "plural_space_format",
			args:          []string{"devices", "test1"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},
		{
			name:          "enrollment_request_space_format",
			args:          []string{"er", "test1"},
			expectedKind:  EnrollmentRequestKind,
			expectedNames: []string{"test1"},
			expectError:   false,
		},

		// TYPE NAME1 NAME2 ... format tests
		{
			name:          "multiple_devices",
			args:          []string{"device", "test1", "test2", "test3"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1", "test2", "test3"},
			expectError:   false,
		},
		{
			name:          "multiple_devices_short_form",
			args:          []string{"dev", "test1", "test2"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1", "test2"},
			expectError:   false,
		},
		{
			name:          "multiple_devices_plural",
			args:          []string{"devices", "test1", "test2"},
			expectedKind:  DeviceKind,
			expectedNames: []string{"test1", "test2"},
			expectError:   false,
		},
		{
			name:          "multiple_enrollment_requests",
			args:          []string{"enrollmentrequests", "req1", "req2"},
			expectedKind:  EnrollmentRequestKind,
			expectedNames: []string{"req1", "req2"},
			expectError:   false,
		},

		// List format tests (no names)
		{
			name:          "list_devices",
			args:          []string{"devices"},
			expectedKind:  DeviceKind,
			expectedNames: []string{},
			expectError:   false,
		},
		{
			name:          "list_devices_short_form",
			args:          []string{"dev"},
			expectedKind:  DeviceKind,
			expectedNames: []string{},
			expectError:   false,
		},
		{
			name:          "list_enrollment_requests",
			args:          []string{"er"},
			expectedKind:  EnrollmentRequestKind,
			expectedNames: []string{},
			expectError:   false,
		},

		// Error cases
		{
			name:          "no_args",
			args:          []string{},
			expectError:   true,
			errorContains: "no arguments provided",
		},
		{
			name:          "invalid_resource_kind",
			args:          []string{"invalidtype"},
			expectError:   true,
			errorContains: "invalid resource kind: invalidtype",
		},
		{
			name:          "invalid_resource_kind_with_name",
			args:          []string{"invalidtype", "test1"},
			expectError:   true,
			errorContains: "invalid resource kind: invalidtype",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, names, err := parseAndValidateKindNameFromArgs(tc.args)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				} else if tc.errorContains != "" && !contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if kind != tc.expectedKind {
				t.Errorf("expected kind %q, got %q", tc.expectedKind, kind)
			}

			if len(names) != len(tc.expectedNames) {
				t.Errorf("expected %d names, got %d", len(tc.expectedNames), len(names))
				return
			}

			for i, expectedName := range tc.expectedNames {
				if names[i] != expectedName {
					t.Errorf("expected name[%d] to be %q, got %q", i, expectedName, names[i])
				}
			}
		})
	}
}

func TestGetOptionsValidation(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		options       *GetOptions
		expectError   bool
		errorContains string
	}{
		// Selector validation tests
		{
			name:          "selector_with_single_resource",
			args:          []string{"device", "test1"},
			options:       &GetOptions{LabelSelector: "app=test"},
			expectError:   true,
			errorContains: "cannot specify label selector when getting specific resources",
		},
		{
			name:          "selector_with_multiple_resources",
			args:          []string{"device", "test1", "test2"},
			options:       &GetOptions{LabelSelector: "app=test"},
			expectError:   true,
			errorContains: "cannot specify label selector when getting specific resources",
		},
		{
			name:          "field_selector_with_specific_resources",
			args:          []string{"device", "test1", "test2"},
			options:       &GetOptions{FieldSelector: "metadata.name=test"},
			expectError:   true,
			errorContains: "cannot specify field selector when getting specific resources",
		},
		{
			name:        "selector_with_list_ok",
			args:        []string{"devices"},
			options:     &GetOptions{LabelSelector: "app=test"},
			expectError: false,
		},

		// Summary validation tests
		{
			name:          "summary_with_specific_devices",
			args:          []string{"device", "test1"},
			options:       &GetOptions{Summary: true},
			expectError:   true,
			errorContains: "cannot specify '--summary' when getting specific devices",
		},
		{
			name:          "summary_with_multiple_devices",
			args:          []string{"device", "test1", "test2"},
			options:       &GetOptions{Summary: true},
			expectError:   true,
			errorContains: "cannot specify '--summary' when getting specific devices",
		},
		{
			name:        "summary_with_device_list_ok",
			args:        []string{"devices"},
			options:     &GetOptions{Summary: true},
			expectError: false,
		},

		// Rendered validation tests
		{
			name:          "rendered_with_multiple_devices",
			args:          []string{"device", "test1", "test2"},
			options:       &GetOptions{Rendered: true},
			expectError:   true,
			errorContains: "'--rendered' can only be used when getting a single device",
		},
		{
			name:        "rendered_with_single_device_ok",
			args:        []string{"device", "test1"},
			options:     &GetOptions{Rendered: true},
			expectError: false,
		},
		{
			name:          "rendered_with_non_device",
			args:          []string{"fleet", "test1"},
			options:       &GetOptions{Rendered: true},
			expectError:   true,
			errorContains: "'--rendered' can only be used when getting a single device",
		},

		// Last seen validation tests
		{
			name:          "last_seen_with_multiple_devices",
			args:          []string{"device", "test1", "test2"},
			options:       &GetOptions{LastSeen: true},
			expectError:   true,
			errorContains: "'--last-seen' can only be used when getting a single device",
		},

		// Single resource restriction tests
		{
			name:          "get_individual_event",
			args:          []string{"event", "test1"},
			expectError:   true,
			errorContains: "you cannot get individual events",
		},
		{
			name:          "get_multiple_events",
			args:          []string{"event", "test1", "test2"},
			expectError:   true,
			errorContains: "you cannot get individual events",
		},
		{
			name:        "list_events_ok",
			args:        []string{"events"},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set up a temporary config file to avoid authentication errors
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "client.yaml")
			writeTestConfig(t, configPath, "")

			opts := DefaultGetOptions()
			opts.ConfigFilePath = configPath
			if tc.options != nil {
				if tc.options.LabelSelector != "" {
					opts.LabelSelector = tc.options.LabelSelector
				}
				if tc.options.FieldSelector != "" {
					opts.FieldSelector = tc.options.FieldSelector
				}
				if tc.options.Summary {
					opts.Summary = tc.options.Summary
				}
				if tc.options.Rendered {
					opts.Rendered = tc.options.Rendered
				}
				if tc.options.LastSeen {
					opts.LastSeen = tc.options.LastSeen
				}
			}

			err := opts.Validate(tc.args)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				} else if tc.errorContains != "" && !contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tc.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

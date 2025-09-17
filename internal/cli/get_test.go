package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/cli/display"
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
func newTestClient(t *testing.T, responses []*http.Response) (*apiclient.ClientWithResponses, *fakeHTTPClient) {
	t.Helper()

	fake := &fakeHTTPClient{responses: responses}
	client, err := apiclient.NewClientWithResponses("http://example.com", apiclient.WithHTTPClient(fake))
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	return client, fake
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

			clientWithResponses, fakeClient := newTestClient(t, responses)

			opts := DefaultGetOptions()
			opts.Output = testCase.output
			opts.Limit = testCase.limit

			// Capture stdout to avoid polluting test output
			_ = captureStdout(t, func() {
				err := opts.handleList(context.Background(), display.NewFormatter(display.OutputFormat(opts.Output)), clientWithResponses, DeviceKind)
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

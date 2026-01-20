package versioning

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersioningTransport_HeaderInjection(t *testing.T) {
	tests := []struct {
		name            string
		baseTransport   http.RoundTripper
		existingHeader  string
		expectedVersion string
	}{
		{"sets header when missing", http.DefaultTransport, "", V1Beta1},
		{"does not overwrite existing", http.DefaultTransport, "v2", "v2"},
		{"works with nil base transport", nil, "", V1Beta1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedVersion string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedVersion = r.Header.Get(HeaderAPIVersion)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			transport := NewTransport(tt.baseTransport, WithAPIV1Beta1())
			client := &http.Client{Transport: transport}

			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			require.NoError(t, err)

			if tt.existingHeader != "" {
				req.Header.Set(HeaderAPIVersion, tt.existingHeader)
			}

			resp, err := client.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			assert.Equal(t, tt.expectedVersion, receivedVersion)
		})
	}
}

func TestVersioningTransport_Deprecation(t *testing.T) {
	pastTimestamp := fmt.Sprintf("@%d", time.Now().Add(-24*time.Hour).Unix())
	futureTimestamp := fmt.Sprintf("@%d", time.Now().Add(24*time.Hour).Unix())

	tests := []struct {
		name              string
		deprecationHeader string
		apiVersionHeader  string
		usePrintf         bool
		expectReport      bool
		expectedVersion   string
	}{
		{"reports past deprecation", pastTimestamp, V1Beta1, true, true, V1Beta1},
		{"reports with fallback version", pastTimestamp, "", true, true, "the version"},
		{"no report when no printf", pastTimestamp, V1Beta1, false, false, ""},
		{"no report for future timestamp", futureTimestamp, V1Beta1, true, false, ""},
		{"no report for invalid format", "invalid-format", V1Beta1, true, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(HeaderDeprecation, tt.deprecationHeader)
				if tt.apiVersionHeader != "" {
					w.Header().Set(HeaderAPIVersion, tt.apiVersionHeader)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			var reported bool
			var reportedVersion string
			printfFn := func(format string, args ...any) {
				reported = true
				if len(args) > 2 {
					reportedVersion = args[2].(string)
				}
			}

			var opts []TransportOption
			opts = append(opts, WithAPIV1Beta1())
			if tt.usePrintf {
				opts = append(opts, WithDeprecationPrintf(printfFn))
			}

			transport := NewTransport(http.DefaultTransport, opts...)
			client := &http.Client{Transport: transport}

			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			if tt.expectReport {
				assert.True(t, reported, "expected deprecation to be reported")
				assert.Equal(t, tt.expectedVersion, reportedVersion)
			} else {
				assert.False(t, reported, "expected deprecation not to be reported")
			}
		})
	}
}

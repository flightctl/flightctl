package versioning

import (
	"net/http"
	"net/http/httptest"
	"testing"

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

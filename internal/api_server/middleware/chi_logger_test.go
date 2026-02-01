package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/flightctl/flightctl/api/versioning"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLogger captures log output for testing
type mockLogger struct {
	messages []string
}

func (m *mockLogger) Print(v ...any) {
	for _, val := range v {
		if s, ok := val.(string); ok {
			m.messages = append(m.messages, s)
		}
	}
}

// Log format: "GET http://example.com/path HTTP/1.1" (version_tag) from 192.0.2.1:1234 - 200 0B in 462ns
var logFormatRegex = regexp.MustCompile(`^"GET http://example\.com/.+ HTTP/1\.1" \(([^)]+)\) from .+ - 200 0B in .+$`)

func TestChiLoggerAPIVersionTag(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		headerValue string
		expectedTag string
	}{
		{
			name:        "valid v1beta1 version",
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "missing header",
			headerValue: "",
			expectedTag: "missing",
		},
		{
			name:        "invalid version string",
			headerValue: "invalid",
			expectedTag: "invalid",
		},
		{
			name:        "v1beta1 with surrounding spaces",
			headerValue: "  v1beta1  ",
			expectedTag: "v1beta1",
		},
		{
			name:        "unknown version v2",
			headerValue: "v2",
			expectedTag: "invalid",
		},
		{
			name:        "random gibberish",
			headerValue: "xyz123",
			expectedTag: "invalid",
		},
		{
			name:        "whitespace only treated as missing",
			headerValue: "   ",
			expectedTag: "missing",
		},
		{
			name:        "partial match rejected",
			headerValue: "v1beta",
			expectedTag: "invalid",
		},
		{
			name:        "url with from in path",
			url:         "/test/from/something",
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "url with from as query param",
			url:         "/test?from=value",
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "url with multiple from occurrences",
			url:         "/from/test/from",
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "url with from and spaces encoded",
			url:         "/path%20from%20test",
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "url containing HTTP/1.1 pattern",
			url:         `/test/HTTP/1.1"/path`,
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
		{
			name:        "url with encoded quotes and HTTP",
			url:         `/%22GET%20http%3A%2F%2Fexample.com%2Fpath%20HTTP%2F1.1%22%20from%20192.0.2.1%3A1234%20-%20200%200B%20in%20462ns`,
			headerValue: "v1beta1",
			expectedTag: "v1beta1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLogger{}
			formatter := ChiLogFormatterWithAPIVersionTag(mock)
			middleware := chimw.RequestLogger(formatter)

			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			url := tt.url
			if url == "" {
				url = "/test"
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.headerValue != "" {
				req.Header.Set(versioning.HeaderAPIVersion, tt.headerValue)
			}
			rr := httptest.NewRecorder()

			middleware(testHandler).ServeHTTP(rr, req)

			require.Len(t, mock.messages, 1, "expected exactly one log message")

			matches := logFormatRegex.FindStringSubmatch(mock.messages[0])
			require.NotNil(t, matches, "log format did not match expected pattern: %s", mock.messages[0])
			require.Len(t, matches, 2, "expected regex to capture version tag")

			assert.Equal(t, tt.expectedTag, matches[1], "captured version tag mismatch")
		})
	}
}

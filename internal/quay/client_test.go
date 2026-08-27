package quay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

// mockQuayServer is a configurable Quay Security API stub.
type mockQuayServer struct {
	mu sync.Mutex

	// Behavior knobs.
	httpStatus int      // HTTP status to return (0 => 200)
	status     string   // Response.Status for a 200 body (ignored when rawBody set)
	response   Response // full body for a 200 response (used when status is empty)
	rawBody    string   // raw body override (for malformed-JSON tests)

	// Captured request state.
	requestCount int
	lastAuth     string
	lastPath     string
	lastQuery    string
}

func newMockQuayServer(t *testing.T, m *mockQuayServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requestCount++
		m.lastAuth = r.Header.Get("Authorization")
		m.lastPath = r.URL.Path
		m.lastQuery = r.URL.RawQuery
		m.mu.Unlock()

		if m.httpStatus != 0 && m.httpStatus != http.StatusOK {
			w.WriteHeader(m.httpStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m.rawBody != "" {
			_, _ = w.Write([]byte(m.rawBody))
			return
		}
		body := m.response
		if m.status != "" {
			body = Response{Status: m.status}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockQuayServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *mockQuayServer) auth() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAuth
}

func (m *mockQuayServer) path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPath
}

func (m *mockQuayServer) query() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastQuery
}

// hostOf returns the "host:port" of a test server URL (scheme stripped),
// suitable for constructing an image reference on that registry.
func hostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func newTestClient(t *testing.T, endpoint string) (*Client, *test.Hook) {
	t.Helper()
	logger, hook := test.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	c, err := NewClient(&config.QuayConfig{Endpoint: endpoint, Token: "test-token"}, logger, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c, hook
}

// hasEntryWithField reports whether any log entry carries field==value.
func hasEntryWithField(hook *test.Hook, field, value string) bool {
	for _, e := range hook.AllEntries() {
		if v, ok := e.Data[field]; ok {
			if s, ok := v.(string); ok && s == value {
				return true
			}
		}
	}
	return false
}

func TestNewClient_NilConfig(t *testing.T) {
	logger, _ := test.NewNullLogger()
	c, err := NewClient(nil, logger, nil)
	require.NoError(t, err)
	require.Nil(t, c, "nil config disables the backend and yields a nil client")
}

func TestNewClient_EmptyEndpoint(t *testing.T) {
	logger, _ := test.NewNullLogger()
	c, err := NewClient(&config.QuayConfig{Endpoint: "", Token: "t"}, logger, nil)
	require.Error(t, err)
	require.Nil(t, c)
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantBase string
		wantHost string
		wantErr  bool
	}{
		{"https with trailing slash", "https://quay.io/", "https://quay.io", "quay.io", false},
		{"schemeless normalized to https", "quay.io", "https://quay.io", "quay.io", false},
		{"http with non-default port preserved", "http://127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:8080", false},
		{"mixed case lowercased", "https://Quay.IO", "https://Quay.IO", "quay.io", false},
		{"explicit default https port stripped", "https://quay.io:443", "https://quay.io:443", "quay.io", false},
		{"explicit default http port stripped", "http://quay.io:80", "http://quay.io:80", "quay.io", false},
		{"empty is an error", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, host, err := parseEndpoint(tt.endpoint)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantBase, base)
			require.Equal(t, tt.wantHost, host)
		})
	}
}

func TestFetchImageSecurity_Success(t *testing.T) {
	score := 9.8
	mock := &mockQuayServer{
		response: Response{
			Status: "scanned",
			Data: &Data{Layer: &Layer{
				Features: []Feature{{
					Name:          "openssl",
					NamespaceName: "rhel:9",
					Vulnerabilities: []Vulnerability{{
						Name:        "RHSA-2024:1234",
						Severity:    "High",
						Link:        "https://access.redhat.com/security/cve/CVE-2024-0001",
						Description: "some flaw",
						Metadata:    Metadata{NVD: NVD{CVSSv3: CVSS{Score: score}}},
					}},
				}},
			}},
		},
	}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.NotNil(t, report)

	require.Equal(t, "Bearer test-token", mock.auth())
	require.Equal(t, "/api/v1/repository/testorg/testrepo/manifest/sha256:abc123/security", mock.path())
	require.Contains(t, mock.query(), "vulnerabilities=true")

	require.Equal(t, "scanned", report.Status)
	require.NotNil(t, report.Data)
	require.NotNil(t, report.Data.Layer)
	require.Len(t, report.Data.Layer.Features, 1)
	feat := report.Data.Layer.Features[0]
	require.Equal(t, "rhel:9", feat.NamespaceName)
	require.Len(t, feat.Vulnerabilities, 1)
	vuln := feat.Vulnerabilities[0]
	require.Equal(t, "RHSA-2024:1234", vuln.Name)
	require.Equal(t, "High", vuln.Severity)
	require.InDelta(t, score, vuln.Metadata.NVD.CVSSv3.Score, 0.001)
}

func TestFetchImageSecurity_NonScannedStatuses(t *testing.T) {
	for _, status := range []string{"queued", "pending", "unsupported", "failed"} {
		t.Run(status, func(t *testing.T) {
			mock := &mockQuayServer{status: status}
			srv := newMockQuayServer(t, mock)
			c, hook := newTestClient(t, srv.URL)

			image := vulnerability.ImageRef{
				Digest: "sha256:abc123",
				Image:  hostOf(srv) + "/testorg/testrepo:latest",
			}
			report, err := c.FetchImageSecurity(context.Background(), image)
			require.NoError(t, err)
			require.Nil(t, report, "non-scanned status yields no report")
			require.Equal(t, 1, mock.count(), "the API is still queried")
			require.True(t, hasEntryWithField(hook, "status", status),
				"the scan status is logged to explain the missing report")
		})
	}
}

func TestFetchImageSecurity_NotFound(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusNotFound}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:notfound",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "a 404 is a skip, not a hard error — sync continues")
	require.Nil(t, report)
	require.True(t, hasEntryWithField(hook, "reason", reasonNotFound))
}

func TestFetchImageSecurity_NonOKStatusLogging(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		wantLevel  logrus.Level
		wantError  bool
	}{
		{"unauthorized logs error and returns error", http.StatusUnauthorized, logrus.ErrorLevel, true},
		{"forbidden logs warn and returns error", http.StatusForbidden, logrus.WarnLevel, true},
		{"server error logs warn and returns error", http.StatusInternalServerError, logrus.WarnLevel, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockQuayServer{httpStatus: tt.httpStatus}
			srv := newMockQuayServer(t, mock)
			c, hook := newTestClient(t, srv.URL)

			image := vulnerability.ImageRef{
				Digest: "sha256:abc123",
				Image:  hostOf(srv) + "/testorg/testrepo:latest",
			}
			report, err := c.FetchImageSecurity(context.Background(), image)
			if tt.wantError {
				require.Error(t, err, "non-404 failures should return errors for caller to decide policy")
				require.Nil(t, report)
			} else {
				require.NoError(t, err)
				require.Nil(t, report)
			}
			require.Equal(t, 1, mock.count())
			last := hook.LastEntry()
			require.NotNil(t, last)
			require.Equal(t, tt.wantLevel, last.Level)
		})
	}
}

func TestFetchImageSecurity_MissingImageReference(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: ""}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, report)
	require.Equal(t, 0, mock.count(), "no request is issued for a missing image reference")
	require.True(t, hasEntryWithField(hook, "reason", reasonMissingImageReference))
}

func TestFetchImageSecurity_MissingDigest(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "", Image: hostOf(srv) + "/testorg/testrepo:latest"}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, report)
	require.Equal(t, 0, mock.count(), "no request is issued when the digest is missing")
	require.True(t, hasEntryWithField(hook, "reason", reasonMissingImageDigest))
}

func TestFetchImageSecurity_RegistryFilter(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  "docker.io/library/nginx:latest",
	}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, report)
	require.Equal(t, 0, mock.count(), "images on other registries are not queried")
	require.True(t, hasEntryWithField(hook, "reason", reasonNotOnConfiguredRegistry))
}

func TestFetchImageSecurity_RegistryFilterNormalization(t *testing.T) {
	// Regression test for mixed-case and explicit default port matching
	tests := []struct {
		name           string
		configEndpoint string
		wantHost       string
	}{
		{"mixed case endpoint lowercased", "https://Quay.IO", "quay.io"},
		{"explicit https default port stripped", "https://quay.io:443", "quay.io"},
		{"explicit http default port stripped", "http://quay.io:80", "quay.io"},
		{"non-default port preserved", "https://quay.io:8443", "quay.io:8443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := test.NewNullLogger()
			c, err := NewClient(&config.QuayConfig{Endpoint: tt.configEndpoint, Token: "test-token"}, logger, nil)
			require.NoError(t, err)
			require.Equal(t, tt.wantHost, c.registryHost, "registryHost should be normalized for case-insensitive comparison")
		})
	}
}

func TestFetchImageSecurity_ImageReferenceNormalization(t *testing.T) {
	// Regression test for image reference normalization (mixed-case, explicit ports)
	// Uses parseImageReference directly to test normalization logic
	tests := []struct {
		name        string
		imageRef    string
		wantHost    string
		configHost  string
		shouldMatch bool
	}{
		{"mixed case image ref normalized to lowercase", "Quay.IO/org/repo:tag", "quay.io", "quay.io", true},
		{"explicit https port 443 stripped", "quay.io:443/org/repo:tag", "quay.io", "quay.io", true},
		{"portless image ref", "quay.io/org/repo:tag", "quay.io", "quay.io", true},
		{"non-default port preserved and matches", "quay.io:8443/org/repo:tag", "quay.io:8443", "quay.io:8443", true},
		{"non-default port mismatch", "quay.io:8443/org/repo:tag", "quay.io:8443", "quay.io", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, repoPath, err := parseImageReference(tt.imageRef)
			require.NoError(t, err)
			require.Equal(t, tt.wantHost, host, "image host should be normalized")
			require.Equal(t, "org/repo", repoPath)

			if tt.shouldMatch {
				require.Equal(t, tt.configHost, host, "normalized image host should match configured host")
			} else {
				require.NotEqual(t, tt.configHost, host, "mismatched hosts should not match")
			}
		})
	}
}

func TestNewClient_NilLoggerDefaults(t *testing.T) {
	c, err := NewClient(&config.QuayConfig{Endpoint: "https://quay.io", Token: "t"}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewClient_CustomHTTPClient(t *testing.T) {
	customClient := &http.Client{Timeout: 10 * time.Second}
	c, err := NewClient(&config.QuayConfig{Endpoint: "https://quay.io", Token: "t"}, nil, customClient)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.Equal(t, customClient, c.httpClient, "custom HTTP client should be preserved")
}

func TestFetchImageSecurity_UnparseableReference(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: "not a valid ref@@@:::"}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "an unparseable reference is a skip, not a hard error")
	require.Nil(t, report)
	require.Equal(t, 0, mock.count(), "no request is issued for an unparseable reference")
}

func TestFetchImageSecurity_TransportError(t *testing.T) {
	// 127.0.0.1:1 is a valid registry host that refuses connections.
	c, _ := newTestClient(t, "http://127.0.0.1:1")

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: "127.0.0.1:1/testorg/testrepo:latest"}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a transport failure is a genuine error")
	require.Nil(t, report)
}

func TestFetchImageSecurity_DecodeError(t *testing.T) {
	mock := &mockQuayServer{rawBody: "{not valid json"}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a malformed response body is a genuine error")
	require.Nil(t, report)
}

func TestFetchImageSecurity_ScannedWithNilData(t *testing.T) {
	// Regression test: Quay's contract is status="scanned" → non-nil Data.Layer
	mock := &mockQuayServer{response: Response{Status: "scanned", Data: nil}}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	report, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "malformed scanned response is a skip, not an error")
	require.Nil(t, report)
	require.True(t, hasEntryWithField(hook, "reason", "malformed_scanned_response"))

	// Verify log level is Warn (check the last entry's level)
	last := hook.LastEntry()
	require.NotNil(t, last)
	require.Equal(t, logrus.WarnLevel, last.Level)
}

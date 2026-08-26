package quay

import (
	"context"
	"encoding/json"
	"errors"
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
	httpStatus     int                      // HTTP status to return (0 => 200)
	status         string                   // Response.Status for a 200 body (ignored when rawBody set)
	response       Response                 // full body for a 200 response (used when status is empty)
	rawBody        string                   // raw body override (for malformed-JSON tests)
	statusSequence []int                    // per-request statuses; index i used for request i, last repeats
	statusByPath   map[string]int           // per-repo-path status (path substring => status)
	delay          time.Duration            // per-request delay (for timeout tests)
	delayByPath    map[string]time.Duration // per-repo-path delay (path substring => delay)

	// Captured request state.
	requestCount int
	lastAuth     string
	lastPath     string
	lastQuery    string

	// Concurrency tracking.
	inFlight    int
	maxInFlight int
}

func newMockQuayServer(t *testing.T, m *mockQuayServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		idx := m.requestCount
		m.requestCount++
		m.lastAuth = r.Header.Get("Authorization")
		m.lastPath = r.URL.Path
		m.lastQuery = r.URL.RawQuery
		m.inFlight++
		if m.inFlight > m.maxInFlight {
			m.maxInFlight = m.inFlight
		}
		status := m.statusFor(idx, r.URL.Path)
		delay := m.delayFor(r.URL.Path)
		m.mu.Unlock()

		defer func() {
			m.mu.Lock()
			m.inFlight--
			m.mu.Unlock()
		}()

		if delay > 0 {
			time.Sleep(delay)
		}

		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
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

// statusFor resolves the HTTP status for a request. Caller holds m.mu.
func (m *mockQuayServer) statusFor(idx int, path string) int {
	for frag, code := range m.statusByPath {
		if strings.Contains(path, frag) {
			return code
		}
	}
	if len(m.statusSequence) > 0 {
		if idx < len(m.statusSequence) {
			return m.statusSequence[idx]
		}
		return m.statusSequence[len(m.statusSequence)-1]
	}
	return m.httpStatus
}

// delayFor resolves the artificial delay for a request. Caller holds m.mu.
func (m *mockQuayServer) delayFor(path string) time.Duration {
	for frag, d := range m.delayByPath {
		if strings.Contains(path, frag) {
			return d
		}
	}
	return m.delay
}

// countEntriesWithField reports how many log entries carry field==value.
func countEntriesWithField(hook *test.Hook, field, value string) int {
	n := 0
	for _, e := range hook.AllEntries() {
		if v, ok := e.Data[field]; ok {
			if s, ok := v.(string); ok && s == value {
				n++
			}
		}
	}
	return n
}

func (m *mockQuayServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *mockQuayServer) maxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxInFlight
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
	c, err := NewClient(&config.QuayConfig{Endpoint: endpoint, Token: "test-token"}, logger)
	require.NoError(t, err)
	require.NotNil(t, c)
	c.backoffBase = time.Millisecond // keep retry tests fast
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
	c, err := NewClient(nil, logger)
	require.NoError(t, err)
	require.Nil(t, c, "nil config disables the backend and yields a nil client")
}

func TestNewClient_EmptyEndpoint(t *testing.T) {
	logger, _ := test.NewNullLogger()
	c, err := NewClient(&config.QuayConfig{Endpoint: "", Token: "t"}, logger)
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
		{"http with port preserved", "http://127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:8080", false},
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
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Equal(t, outcomeScanned, res.Outcome)
	require.NotNil(t, res.Report)
	require.Equal(t, 1, res.Attempts)

	require.Equal(t, "Bearer test-token", mock.auth())
	require.Equal(t, "/api/v1/repository/testorg/testrepo/manifest/sha256:abc123/security", mock.path())
	require.Contains(t, mock.query(), "vulnerabilities=true")

	require.Equal(t, "scanned", res.Report.Status)
	require.NotNil(t, res.Report.Data)
	require.NotNil(t, res.Report.Data.Layer)
	require.Len(t, res.Report.Data.Layer.Features, 1)
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
			res, err := c.FetchImageSecurity(context.Background(), image)
			require.NoError(t, err)
			require.Nil(t, res.Report, "non-scanned status yields no report")
			require.Equal(t, outcomeSkipped, res.Outcome)
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
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "a 404 is a skip, not a hard error — sync continues")
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkipped, res.Outcome)
	require.True(t, hasEntryWithField(hook, "reason", reasonNotFound))
	require.True(t, hasEntryWithField(hook, "event", eventScanSkipped))
}

func TestFetchImageSecurity_Unauthorized(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusUnauthorized}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "401 must surface as a terminal error, not a skip")
	require.ErrorIs(t, err, ErrQuayAuth)
	require.Nil(t, res.Report)
	require.Equal(t, 1, mock.count(), "401 is not retried")

	last := hook.LastEntry()
	require.NotNil(t, last)
	require.Equal(t, logrus.ErrorLevel, last.Level)
	require.True(t, hasEntryWithField(hook, "event", eventAuthError))
}

func TestFetchImageSecurity_Forbidden(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusForbidden}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "403 skips the image with a warning; it is not a hard error")
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkipped, res.Outcome)
	require.Equal(t, 1, mock.count(), "403 is not retried")

	last := hook.LastEntry()
	require.NotNil(t, last)
	require.Equal(t, logrus.WarnLevel, last.Level)
	require.True(t, hasEntryWithField(hook, "reason", reasonForbidden))
}

func TestFetchImageSecurity_UnexpectedStatus(t *testing.T) {
	// An unexpected, non-retryable status (not 401/403/404, not 429/5xx) must
	// degrade gracefully: the image is skipped with a warn, not a hard error.
	mock := &mockQuayServer{httpStatus: http.StatusBadRequest}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "an unexpected status skips the image rather than failing the scan")
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkipped, res.Outcome)
	require.Equal(t, 1, mock.count(), "an unexpected 4xx is not retried")

	last := hook.LastEntry()
	require.NotNil(t, last)
	require.Equal(t, logrus.WarnLevel, last.Level)
	require.True(t, hasEntryWithField(hook, "event", eventScanSkipped))
}

func TestFetchImageSecurity_Retries429ThenSuccess(t *testing.T) {
	mock := &mockQuayServer{
		statusSequence: []int{http.StatusTooManyRequests, http.StatusOK},
		response:       Response{Status: "scanned", Data: &Data{Layer: &Layer{}}},
	}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.NotNil(t, res.Report)
	require.Equal(t, 2, res.Attempts, "one retry after the 429")
	require.Equal(t, 2, mock.count())
	require.True(t, hasEntryWithField(hook, "event", eventRateLimited))
}

func TestFetchImageSecurity_Retries5xxThenSuccess(t *testing.T) {
	mock := &mockQuayServer{
		statusSequence: []int{http.StatusInternalServerError, http.StatusOK},
		response:       Response{Status: "scanned", Data: &Data{Layer: &Layer{}}},
	}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.NotNil(t, res.Report)
	require.Equal(t, 2, res.Attempts, "one retry after the 500")
}

func TestFetchImageSecurity_RetriesExhausted(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusServiceUnavailable}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a persistent 5xx fails after exhausting retries")
	require.Nil(t, res.Report)
	require.Equal(t, maxRetries, mock.count(), "exactly maxRetries attempts are made")
	require.ErrorContains(t, err, "max retries exceeded querying quay security api",
		"the exhaustion error names the retry contract")
	require.ErrorContains(t, err, "status 503", "the last transient failure is wrapped")
}

func TestFetchImageSecurity_ContextCancelledDuringBackoff(t *testing.T) {
	// First attempt returns a retryable 503, then the context is cancelled while
	// the client sleeps in its backoff window — exercising the ctx.Done() branch
	// between attempts (distinct from a cancellation before the first request).
	mock := &mockQuayServer{httpStatus: http.StatusServiceUnavailable}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)
	c.backoffBase = 200 * time.Millisecond // wide enough to cancel mid-backoff

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(20*time.Millisecond, cancel)

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	_, err := c.FetchImageSecurity(ctx, image)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, mock.count(), "cancellation during backoff stops further attempts")
}

func TestFetchImageSecurity_TimeoutRetriesThenFails(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusOK, delay: 200 * time.Millisecond, status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)
	c.httpClient.Timeout = 20 * time.Millisecond

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a persistent timeout fails after retries")
	require.Nil(t, res.Report)
	require.Equal(t, maxRetries, mock.count(), "timeouts are retried up to maxRetries")
	require.ErrorContains(t, err, "timed out", "the timeout is surfaced in the wrapped error")
}

func TestFetchImageSecurity_MissingImageReference(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: ""}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkippedRegistry, res.Outcome)
	require.Equal(t, 0, mock.count(), "no request is issued for a missing image reference")
	require.True(t, hasEntryWithField(hook, "reason", reasonMissingImageReference))
}

func TestFetchImageSecurity_MissingDigest(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, hook := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "", Image: hostOf(srv) + "/testorg/testrepo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkippedRegistry, res.Outcome)
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
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err)
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkippedRegistry, res.Outcome)
	require.Equal(t, 0, mock.count(), "images on other registries are not queried")
	require.True(t, hasEntryWithField(hook, "reason", reasonNotOnConfiguredRegistry))
}

func TestNewClient_NilLoggerDefaults(t *testing.T) {
	c, err := NewClient(&config.QuayConfig{Endpoint: "https://quay.io", Token: "t"}, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestFetchImageSecurity_UnparseableReference(t *testing.T) {
	mock := &mockQuayServer{status: "scanned"}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: "not a valid ref@@@:::"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.NoError(t, err, "an unparseable reference is a skip, not a hard error")
	require.Nil(t, res.Report)
	require.Equal(t, outcomeSkippedRegistry, res.Outcome)
	require.Equal(t, 0, mock.count(), "no request is issued for an unparseable reference")
}

func TestFetchImageSecurity_TransportError(t *testing.T) {
	// 127.0.0.1:1 is a valid registry host that refuses connections. A
	// connection refusal is not a timeout, so it is not retried.
	c, _ := newTestClient(t, "http://127.0.0.1:1")

	image := vulnerability.ImageRef{Digest: "sha256:abc123", Image: "127.0.0.1:1/testorg/testrepo:latest"}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a transport failure is a genuine error")
	require.Nil(t, res.Report)
}

func TestFetchImageSecurity_DecodeError(t *testing.T) {
	mock := &mockQuayServer{rawBody: "{not valid json"}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	image := vulnerability.ImageRef{
		Digest: "sha256:abc123",
		Image:  hostOf(srv) + "/testorg/testrepo:latest",
	}
	res, err := c.FetchImageSecurity(context.Background(), image)
	require.Error(t, err, "a malformed response body is a genuine error")
	require.Nil(t, res.Report)
	require.Equal(t, 1, mock.count(), "a 200 with a bad body is not retried")
}

func TestFetchImageSecurity_ContextCancelled(t *testing.T) {
	mock := &mockQuayServer{httpStatus: http.StatusServiceUnavailable}
	srv := newMockQuayServer(t, mock)
	c, _ := newTestClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	image := vulnerability.ImageRef{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"}
	_, err := c.FetchImageSecurity(ctx, image)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled"))
}

package trustifyv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----------------------------------------------------------------

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// makeAnalysisResponse builds an AnalysisResponse payload for one PURL with a
// single CVE finding.
func makeAnalysisResponse(purl, cveID, status, advisoryID, severity string, score float64, published *time.Time) AnalysisResponse {
	desc := "test description"
	return AnalysisResponse{
		purl: {
			Details: []AnalysisDetails{
				{
					Identifier:  cveID,
					Description: &desc,
					Status: map[string][]AnalysisAdvisory{
						status: {
							{
								Identifier: advisoryID,
								Published:  published,
								Scores: []Score{
									{
										Value:    score,
										Severity: Severity(severity),
										Type:     "3.1",
									},
								},
							},
						},
					},
				},
			},
			Warnings: []string{},
		},
	}
}

// newTrustifyServer starts an httptest.Server that serves the provided payload
// and captures the incoming request for inspection.
type requestCapture struct {
	Method string
	URL    string
	Body   []byte
	Header http.Header
}

func newTrustifyServer(t *testing.T, statusCode int, payload []byte) (*httptest.Server, *requestCapture) {
	t.Helper()
	reqCap := &requestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCap.Method = r.Method
		reqCap.URL = r.URL.String()
		reqCap.Header = r.Header.Clone()
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err == nil {
			reqCap.Body = body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, reqCap
}

// newOIDCServer starts a minimal OIDC discovery + token server.
func newOIDCServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			doc := map[string]string{"token_endpoint": srv.URL + "/token"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(doc)
		case "/token":
			resp := map[string]any{
				"access_token": "fake-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---- NewVulnerabilityClient -------------------------------------------------

func TestNewVulnerabilityClient_NilConfig(t *testing.T) {
	c, err := NewVulnerabilityClient(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, c, "When config is nil the client should be nil (feature disabled)")
}

func TestNewVulnerabilityClient_NoneAuthMode(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, AnalysisResponse{}))
	cfg := &config.TrustifyConfig{
		Endpoint: srv.URL,
		Auth:     &config.TrustifyAuthConfig{Mode: AuthModeNone},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_EmptyAuthMode(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, AnalysisResponse{}))
	cfg := &config.TrustifyConfig{
		Endpoint: srv.URL,
		Auth:     &config.TrustifyAuthConfig{},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_NilAuth(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, AnalysisResponse{}))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_ClientCredentials_OIDCDiscovery(t *testing.T) {
	oidcSrv := newOIDCServer(t)
	trustifySrv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, AnalysisResponse{}))

	cfg := &config.TrustifyConfig{
		Endpoint: trustifySrv.URL,
		Auth: &config.TrustifyAuthConfig{
			Mode:          AuthModeClientCredentials,
			OIDCIssuerURL: oidcSrv.URL,
			ClientID:      "my-client",
			ClientSecret:  "my-secret",
		},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_ClientCredentials_EmptyIssuerURL(t *testing.T) {
	cfg := &config.TrustifyConfig{
		Endpoint: "http://trustify.example.com",
		Auth: &config.TrustifyAuthConfig{
			Mode:          AuthModeClientCredentials,
			OIDCIssuerURL: "",
		},
	}
	_, err := NewVulnerabilityClient(context.Background(), cfg)
	require.Error(t, err)

	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthModeClientCredentials, authErr.Mode)
}

func TestNewVulnerabilityClient_UnsupportedAuthMode(t *testing.T) {
	cfg := &config.TrustifyConfig{
		Endpoint: "http://trustify.example.com",
		Auth:     &config.TrustifyAuthConfig{Mode: "magic-token"},
	}
	_, err := NewVulnerabilityClient(context.Background(), cfg)
	require.Error(t, err)

	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, "magic-token", authErr.Mode)
}

// ---- GetVulnerabilities (none auth) ----------------------------------------

func TestGetVulnerabilities_ReturnsFindings(t *testing.T) {
	published := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	digest := "sha256:abc123"
	purl := ociPURL(digest)
	payload := makeAnalysisResponse(purl, "CVE-2024-1234", "affected", "RHSA-2024:001", "high", 8.1, &published)

	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, payload))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	findings, err := c.GetVulnerabilities(context.Background(), digest)
	require.NoError(t, err)
	require.Len(t, findings, 1)

	f := findings[0]
	require.Equal(t, digest, f.ImageDigest)
	require.Equal(t, "CVE-2024-1234", f.CVEID)
	require.Equal(t, "affected", f.Status)
	require.Equal(t, "high", f.Severity)
	require.Equal(t, "RHSA-2024:001", f.AdvisoryID)
	require.Equal(t, "test description", f.Description)
	require.NotNil(t, f.CVSSScore)
	require.InDelta(t, 8.1, *f.CVSSScore, 0.001)
	require.NotNil(t, f.PublishedAt)
	require.True(t, f.PublishedAt.Equal(published))
}

func TestGetVulnerabilities_EmptyResponse(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, AnalysisResponse{}))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	findings, err := c.GetVulnerabilities(context.Background(), "sha256:deadbeef")
	require.NoError(t, err)
	require.Empty(t, findings)
}

func TestGetVulnerabilities_MultipleCVEs(t *testing.T) {
	digest := "sha256:multi"
	purl := ociPURL(digest)

	desc1, desc2 := "desc1", "desc2"
	payload := AnalysisResponse{
		purl: {
			Details: []AnalysisDetails{
				{
					Identifier:  "CVE-2024-0001",
					Description: &desc1,
					Status: map[string][]AnalysisAdvisory{
						"affected": {{Identifier: "RHSA-2024:001", Scores: []Score{{Value: 9.8, Severity: "critical", Type: "3.1"}}}},
					},
				},
				{
					Identifier:  "CVE-2024-0002",
					Description: &desc2,
					Status: map[string][]AnalysisAdvisory{
						"under_investigation": {{Identifier: "RHSA-2024:002", Scores: []Score{{Value: 5.4, Severity: "medium", Type: "3.1"}}}},
					},
				},
			},
			Warnings: []string{},
		},
	}

	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, payload))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	findings, err := c.GetVulnerabilities(context.Background(), digest)
	require.NoError(t, err)
	require.Len(t, findings, 2)
	for _, f := range findings {
		require.Equal(t, digest, f.ImageDigest)
	}
}

func TestGetVulnerabilities_CVSSScorePicksHighest(t *testing.T) {
	digest := "sha256:scores"
	purl := ociPURL(digest)
	desc := "desc"
	payload := AnalysisResponse{
		purl: {
			Details: []AnalysisDetails{
				{
					Identifier:  "CVE-2024-9999",
					Description: &desc,
					Status: map[string][]AnalysisAdvisory{
						"affected": {
							{
								Identifier: "RHSA-2024:001",
								Scores: []Score{
									{Value: 6.5, Severity: "medium", Type: "2.0"},
									{Value: 8.8, Severity: "high", Type: "3.1"},
									{Value: 7.0, Severity: "high", Type: "3.0"},
								},
							},
						},
					},
				},
			},
		},
	}

	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, payload))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	findings, err := c.GetVulnerabilities(context.Background(), digest)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.NotNil(t, findings[0].CVSSScore)
	require.InDelta(t, 8.8, *findings[0].CVSSScore, 0.001)
	require.Equal(t, "high", findings[0].Severity)
}

func TestGetVulnerabilities_NoScores(t *testing.T) {
	digest := "sha256:noscores"
	purl := ociPURL(digest)
	desc := "desc"
	payload := AnalysisResponse{
		purl: {
			Details: []AnalysisDetails{
				{
					Identifier:  "CVE-2024-0001",
					Description: &desc,
					Status: map[string][]AnalysisAdvisory{
						"affected": {{Identifier: "RHSA-2024:001"}},
					},
				},
			},
		},
	}

	srv, _ := newTrustifyServer(t, http.StatusOK, mustJSON(t, payload))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	findings, err := c.GetVulnerabilities(context.Background(), digest)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Nil(t, findings[0].CVSSScore)
	require.Empty(t, findings[0].Severity)
}

// ---- Error handling ---------------------------------------------------------

func TestGetVulnerabilities_ServerError(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusInternalServerError, []byte(`{"error":"boom"}`))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilities(context.Background(), "sha256:err")
	require.Error(t, err)

	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
	require.Contains(t, connErr.Error(), "500")
}

func TestGetVulnerabilities_InvalidJSON(t *testing.T) {
	srv, _ := newTrustifyServer(t, http.StatusOK, []byte(`not-json`))
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilities(context.Background(), "sha256:bad")
	require.Error(t, err)

	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
}

func TestGetVulnerabilities_Unreachable(t *testing.T) {
	cfg := &config.TrustifyConfig{Endpoint: "http://127.0.0.1:1"}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilities(context.Background(), "sha256:x")
	require.Error(t, err)

	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
}

// ---- client-credentials auth ------------------------------------------------

func TestGetVulnerabilities_ClientCredentials_SendsBearerToken(t *testing.T) {
	oidcSrv := newOIDCServer(t)

	var capturedAuth string
	trustifySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AnalysisResponse{})
	}))
	t.Cleanup(trustifySrv.Close)

	cfg := &config.TrustifyConfig{
		Endpoint: trustifySrv.URL,
		Auth: &config.TrustifyAuthConfig{
			Mode:          AuthModeClientCredentials,
			OIDCIssuerURL: oidcSrv.URL,
			ClientID:      "my-client",
			ClientSecret:  "my-secret",
		},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilities(context.Background(), "sha256:auth")
	require.NoError(t, err)
	require.Equal(t, "Bearer fake-token", capturedAuth)
}

// ---- ociPURL ----------------------------------------------------------------

func TestOciPURL(t *testing.T) {
	tests := []struct {
		name   string
		digest string
		want   string
	}{
		{
			name:   "When digest has sha256 prefix it should produce a valid OCI PURL",
			digest: "sha256:abc123",
			want:   "pkg:oci/unknown@sha256:abc123",
		},
		{
			name:   "When digest is empty the PURL still has the prefix",
			digest: "",
			want:   "pkg:oci/unknown@",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ociPURL(tt.digest))
		})
	}
}

// ---- bestScore --------------------------------------------------------------

func TestBestScore(t *testing.T) {
	tests := []struct {
		name      string
		scores    []Score
		wantNil   bool
		wantValue float64
	}{
		{
			name:    "When scores is empty it should return nil",
			scores:  nil,
			wantNil: true,
		},
		{
			name:      "When there is one score it should return it",
			scores:    []Score{{Value: 7.5}},
			wantValue: 7.5,
		},
		{
			name:      "When multiple scores it should return the highest",
			scores:    []Score{{Value: 5.0}, {Value: 9.8}, {Value: 7.0}},
			wantValue: 9.8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bestScore(tt.scores)
			if tt.wantNil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.InDelta(t, tt.wantValue, got.Value, 0.001)
		})
	}
}

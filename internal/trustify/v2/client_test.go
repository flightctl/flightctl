package trustifyv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/require"
)

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

// mockTrustifyServer creates a mock Trustify server that handles the 3-step flow:
// 1. GET /api/v2/purl?q=<digest> - returns PURLs containing the digest
// 2. GET /api/v2/sbom/by-package?purl=<purl> - returns SBOMs containing the package
// 3. GET /api/v2/sbom/{id}/advisory - returns advisories for the SBOM
type mockTrustifyServer struct {
	purls      map[string]PurlSummary    // digest -> PURL (OCI PURL containing the digest)
	sboms      map[string]SbomSummary    // purl -> SBOM
	advisories map[string][]SbomAdvisory // sbomID -> advisories
}

func newMockTrustifyServer(t *testing.T, mock *mockTrustifyServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Handle PURL list: GET /api/v2/purl?q=<digest1|digest2|...>
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/purl" {
			query := r.URL.Query().Get("q")
			var items []PurlSummary
			digests := strings.Split(query, "|")
			for _, d := range digests {
				for digestKey, purl := range mock.purls {
					if strings.Contains(digestKey, d) || strings.Contains(d, digestKey) {
						items = append(items, purl)
					}
				}
			}
			resp := PurlListResponse{Items: items, Total: int64(len(items))}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Handle SBOM by package: GET /api/v2/sbom/by-package?purl=<purl>
		if r.Method == http.MethodGet && r.URL.Path == "/api/v2/sbom/by-package" {
			purl := r.URL.Query().Get("purl")
			var items []SbomSummary
			for purlKey, sbom := range mock.sboms {
				if strings.Contains(purl, purlKey) || strings.Contains(purlKey, purl) {
					items = append(items, sbom)
				}
			}
			resp := SbomListResponse{Items: items, Total: int64(len(items))}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Handle SBOM advisories: GET /api/v2/sbom/{id}/advisory
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/advisory") {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) >= 5 {
				sbomID := parts[4] // /api/v2/sbom/{id}/advisory
				if advisories, ok := mock.advisories[sbomID]; ok {
					_ = json.NewEncoder(w).Encode(advisories)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}

		http.NotFound(w, r)
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
	srv := newMockTrustifyServer(t, &mockTrustifyServer{})
	cfg := &config.TrustifyConfig{
		Endpoint: srv.URL,
		Auth:     &config.TrustifyAuthConfig{Mode: AuthModeNone},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_EmptyAuthMode(t *testing.T) {
	srv := newMockTrustifyServer(t, &mockTrustifyServer{})
	cfg := &config.TrustifyConfig{
		Endpoint: srv.URL,
		Auth:     &config.TrustifyAuthConfig{},
	}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_NilAuth(t *testing.T) {
	srv := newMockTrustifyServer(t, &mockTrustifyServer{})
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewVulnerabilityClient_ClientCredentials_OIDCDiscovery(t *testing.T) {
	oidcSrv := newOIDCServer(t)
	trustifySrv := newMockTrustifyServer(t, &mockTrustifyServer{})

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

// ---- GetVulnerabilitiesForDigests -------------------------------------------

func TestGetVulnerabilitiesForDigests_ReturnsFindings(t *testing.T) {
	digest := "sha256:abc123def456"
	sbomID := "urn:uuid:test-sbom-1"
	purl := "pkg:oci/test-image@sha256:abc123def456?repository_url=quay.io/test"
	published := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	title := "HTTP/2 Rapid Reset Attack"

	mock := &mockTrustifyServer{
		purls: map[string]PurlSummary{
			"abc123def456": {
				Uuid: "purl-1",
				Purl: purl,
			},
		},
		sboms: map[string]SbomSummary{
			purl: {
				Id:   sbomID,
				Name: "test-image",
			},
		},
		advisories: map[string][]SbomAdvisory{
			sbomID: {
				{
					Uuid:       "adv-1",
					Identifier: "https://www.redhat.com/#RHSA-2024:1234",
					DocumentId: "RHSA-2024:1234",
					Title:      &title,
					Published:  &published,
					Status: []SbomStatus{
						{
							Identifier:      "CVE-2024-1234",
							Status:          "affected",
							AverageSeverity: High,
							AverageScore:    7.5,
						},
					},
				},
			},
		},
	}

	srv := newMockTrustifyServer(t, mock)
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	results, err := c.GetVulnerabilitiesForDigests(context.Background(), []string{digest})
	require.NoError(t, err)
	require.Contains(t, results, digest)

	findings := results[digest]
	require.Len(t, findings, 1)

	f := findings[0]
	require.Equal(t, digest, f.ImageDigest)
	require.Equal(t, "CVE-2024-1234", f.CVEID)
	require.Equal(t, "affected", f.Status)
	require.Equal(t, "high", f.Severity)
	require.NotNil(t, f.CVSSScore)
	require.InDelta(t, 7.5, *f.CVSSScore, 0.001)
	require.Equal(t, title, f.Description)
}

func TestGetVulnerabilitiesForDigests_EmptyDigests(t *testing.T) {
	srv := newMockTrustifyServer(t, &mockTrustifyServer{})
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	results, err := c.GetVulnerabilitiesForDigests(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestGetVulnerabilitiesForDigests_NoMatchingPURL(t *testing.T) {
	mock := &mockTrustifyServer{
		purls: map[string]PurlSummary{}, // No PURLs
		sboms: map[string]SbomSummary{},
	}

	srv := newMockTrustifyServer(t, mock)
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	results, err := c.GetVulnerabilitiesForDigests(context.Background(), []string{"sha256:unknown"})
	require.NoError(t, err)
	require.Contains(t, results, "sha256:unknown")
	require.Nil(t, results["sha256:unknown"], "Digest without PURL should have nil findings")
}

func TestGetVulnerabilitiesForDigests_MultipleDigests(t *testing.T) {
	digest1 := "sha256:aaaa1111"
	digest2 := "sha256:bbbb2222"
	purl1 := "pkg:oci/image-1@sha256:aaaa1111?repository_url=quay.io/test"
	purl2 := "pkg:oci/image-2@sha256:bbbb2222?repository_url=quay.io/test"
	sbomID1 := "urn:uuid:sbom-1"
	sbomID2 := "urn:uuid:sbom-2"

	mock := &mockTrustifyServer{
		purls: map[string]PurlSummary{
			"aaaa1111": {Uuid: "purl-1", Purl: purl1},
			"bbbb2222": {Uuid: "purl-2", Purl: purl2},
		},
		sboms: map[string]SbomSummary{
			purl1: {Id: sbomID1, Name: "image-1"},
			purl2: {Id: sbomID2, Name: "image-2"},
		},
		advisories: map[string][]SbomAdvisory{
			sbomID1: {
				{
					DocumentId: "RHSA-2024:0001",
					Identifier: "https://access.redhat.com/errata/RHSA-2024:0001",
					Status:     []SbomStatus{{Identifier: "CVE-2024-0001", Status: "affected", AverageSeverity: High, AverageScore: 8.0}},
				},
			},
			sbomID2: {
				{
					DocumentId: "RHSA-2024:0002",
					Identifier: "https://access.redhat.com/errata/RHSA-2024:0002",
					Status:     []SbomStatus{{Identifier: "CVE-2024-0002", Status: "fixed", AverageSeverity: Medium, AverageScore: 5.0}},
				},
			},
		},
	}

	srv := newMockTrustifyServer(t, mock)
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	results, err := c.GetVulnerabilitiesForDigests(context.Background(), []string{digest1, digest2})
	require.NoError(t, err)

	require.Len(t, results[digest1], 1)
	require.Equal(t, "CVE-2024-0001", results[digest1][0].CVEID)

	require.Len(t, results[digest2], 1)
	require.Equal(t, "CVE-2024-0002", results[digest2][0].CVEID)
}

func TestGetVulnerabilitiesForDigests_MultipleStatusEntries(t *testing.T) {
	digest := "sha256:multi123"
	purl := "pkg:oci/image-multi@sha256:multi123?repository_url=quay.io/test"
	sbomID := "urn:uuid:sbom-multi"

	mock := &mockTrustifyServer{
		purls: map[string]PurlSummary{
			"multi123": {Uuid: "purl-multi", Purl: purl},
		},
		sboms: map[string]SbomSummary{
			purl: {Id: sbomID, Name: "image-multi"},
		},
		advisories: map[string][]SbomAdvisory{
			sbomID: {
				{
					DocumentId: "RHSA-2024:9999",
					Identifier: "https://access.redhat.com/errata/RHSA-2024:9999",
					Status: []SbomStatus{
						{Identifier: "CVE-2024-9999", Status: "affected", AverageSeverity: High, AverageScore: 8.0},
						{Identifier: "CVE-2024-9998", Status: "fixed", AverageSeverity: High, AverageScore: 8.0},
					},
				},
			},
		},
	}

	srv := newMockTrustifyServer(t, mock)
	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	results, err := c.GetVulnerabilitiesForDigests(context.Background(), []string{digest})
	require.NoError(t, err)

	// Should have 2 findings (one for each status entry)
	require.Len(t, results[digest], 2)
}

func TestGetVulnerabilitiesForDigests_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.TrustifyConfig{Endpoint: srv.URL}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilitiesForDigests(context.Background(), []string{"sha256:test"})
	require.Error(t, err)

	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
}

func TestGetVulnerabilitiesForDigests_Unreachable(t *testing.T) {
	cfg := &config.TrustifyConfig{Endpoint: "http://127.0.0.1:1"}
	c, err := NewVulnerabilityClient(context.Background(), cfg)
	require.NoError(t, err)

	_, err = c.GetVulnerabilitiesForDigests(context.Background(), []string{"sha256:x"})
	require.Error(t, err)

	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
}

// ---- helper functions -------------------------------------------------------

func TestStripSHA256Prefix(t *testing.T) {
	require.Equal(t, "abc123", stripSHA256Prefix("sha256:abc123"))
	require.Equal(t, "abc123", stripSHA256Prefix("abc123"))
	require.Equal(t, "", stripSHA256Prefix("sha256:"))
	require.Equal(t, "", stripSHA256Prefix(""))
}

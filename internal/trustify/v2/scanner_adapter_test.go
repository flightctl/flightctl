package trustifyv2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeClient records the digests it is asked for and returns canned findings.
type fakeClient struct {
	results    map[string][]Finding
	err        error
	gotDigests []string
}

func (c *fakeClient) GetVulnerabilitiesForDigests(_ context.Context, digests []string) (map[string][]Finding, error) {
	c.gotDigests = digests
	if c.err != nil {
		return nil, c.err
	}
	out := make(map[string][]Finding, len(digests))
	for _, d := range digests {
		out[d] = c.results[d]
	}
	return out, nil
}

func (c *fakeClient) UploadSBOM(_ context.Context, _ []byte, _ string) error { return nil }

// stubClient is a stateless VulnerabilityClient for concurrency tests where a
// shared results/gotDigests field would itself race.
type stubClient struct{}

func (c *stubClient) GetVulnerabilitiesForDigests(_ context.Context, _ []string) (map[string][]Finding, error) {
	return map[string][]Finding{}, nil
}

func (c *stubClient) UploadSBOM(_ context.Context, _ []byte, _ string) error { return nil }

func TestTrustifyScanner_ScanImages_PassesDigestsOnly(t *testing.T) {
	req := require.New(t)

	client := &fakeClient{results: map[string][]Finding{
		"sha256:aaaa": {{ImageDigest: "sha256:aaaa", CVEID: "CVE-1", Status: "affected", Severity: "high"}},
	}}
	s := &trustifyScanner{client: client}

	_, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:aaaa", Image: "quay.io/org/repo:tag"},
		{Digest: "sha256:bbbb", Image: ""},
	})
	req.NoError(err)

	// The image reference is ignored; only digests reach the client.
	req.Equal([]string{"sha256:aaaa", "sha256:bbbb"}, client.gotDigests)
}

func TestTrustifyScanner_ScanImages_PropagatesClientError(t *testing.T) {
	req := require.New(t)

	s := &trustifyScanner{client: &fakeClient{err: errors.New("connection refused")}}

	_, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{{Digest: "sha256:aaaa"}})
	req.Error(err)
}

func TestTrustifyScanner_ScanImages_PreservesNilForMissingSBOM(t *testing.T) {
	req := require.New(t)

	client := &fakeClient{results: map[string][]Finding{"sha256:aaaa": nil}}
	s := &trustifyScanner{client: client}

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{{Digest: "sha256:aaaa"}})
	req.NoError(err)
	req.Nil(out["sha256:aaaa"])
}

func TestTrustifyScanner_ScanImages_SkipsUnnormalizableFindings(t *testing.T) {
	req := require.New(t)

	digest := "sha256:aaaa"
	client := &fakeClient{results: map[string][]Finding{
		digest: {
			{ImageDigest: digest, CVEID: "CVE-BAD-STATUS", Status: "under_investigation", Severity: "critical"},
			{ImageDigest: digest, CVEID: "CVE-BAD-SEVERITY", Status: "affected", Severity: "severe"},
			{ImageDigest: digest, CVEID: "CVE-OK", Status: "affected", Severity: "high"},
		},
	}}
	s := &trustifyScanner{client: client}

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{{Digest: digest}})
	req.NoError(err)
	req.Len(out[digest], 1)
	req.Equal("CVE-OK", out[digest][0].CveID)
}

func TestTrustifyScanner_toVulnFinding_ConversionParity(t *testing.T) {
	req := require.New(t)

	score := 8.5
	published := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	issuerID := uuid.New()
	cpe := "cpe:/o:redhat"
	site := "https://access.redhat.com"

	vf, err := toVulnFinding(Finding{
		ImageDigest: "sha256:aaaa",
		CVEID:       "CVE-2024-1234",
		Status:      "affected",
		Severity:    "critical",
		AdvisoryID:  "RHSA-2024:9999",
		Description: "Remote code execution",
		CVSSScore:   &score,
		PublishedAt: &published,
		Issuer:      &Issuer{Id: issuerID, Name: "Red Hat", CpeKey: &cpe, Website: &site},
	})
	req.NoError(err)

	req.Equal("CVE-2024-1234", vf.CveID)
	req.Equal("sha256:aaaa", vf.ImageDigest)
	req.Equal("affected", vf.Status)
	req.Equal("Critical", vf.Severity)
	req.Equal(&score, vf.CvssScore)
	req.Equal(&published, vf.PublishedAt)
	req.Equal("Remote code execution", vf.Description)
	req.NotNil(vf.AdvisoryID)
	req.Equal("RHSA-2024:9999", *vf.AdvisoryID)
	req.NotNil(vf.Issuer)
	req.Equal(issuerID.String(), vf.Issuer.ID)
	req.Equal("Red Hat", vf.Issuer.Name)
	req.Equal(&cpe, vf.Issuer.CpeKey)
	req.Equal(&site, vf.Issuer.Website)
}

func TestTrustifyScanner_toVulnFinding_EmptyOptionalFields(t *testing.T) {
	req := require.New(t)

	vf, err := toVulnFinding(Finding{
		ImageDigest: "sha256:dddd",
		CVEID:       "CVE-2024-5678",
		Status:      "not_affected",
		Severity:    "none",
	})
	req.NoError(err)
	req.Nil(vf.AdvisoryID, "empty AdvisoryID should map to nil")
	req.Nil(vf.Issuer, "nil Issuer should stay nil")
	req.Empty(vf.Description)
	req.Nil(vf.CvssScore)
	req.Nil(vf.PublishedAt)
	req.Equal("not_affected", vf.Status)
	req.Equal("None", vf.Severity)
}

func TestNormalizeTrustifyStatus(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "When status is affected it should map to affected", in: "Affected", want: "affected"},
		{name: "When status is not_affected it should map to not_affected", in: "not_affected", want: "not_affected"},
		{name: "When status is fixed it should map to fixed", in: "FIXED", want: "fixed"},
		{name: "When status is unknown it should map to unknown", in: "unknown", want: "unknown"},
		{name: "When status is empty it should map to unknown", in: "", want: "unknown"},
		{name: "When status is unsupported it should error", in: "under_investigation", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			got, err := normalizeTrustifyStatus(tt.in)
			if tt.wantErr {
				req.Error(err)
				return
			}
			req.NoError(err)
			req.Equal(tt.want, got)
		})
	}
}

func TestNormalizeTrustifySeverity(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "When severity is critical it should map to Critical", in: "critical", want: "Critical"},
		{name: "When severity is high it should map to High", in: "HIGH", want: "High"},
		{name: "When severity is medium it should map to Medium", in: "Medium", want: "Medium"},
		{name: "When severity is low it should map to Low", in: "low", want: "Low"},
		{name: "When severity is none it should map to None", in: "none", want: "None"},
		{name: "When severity is unknown it should map to Unknown", in: "unknown", want: "Unknown"},
		{name: "When severity is empty it should map to Unknown", in: "", want: "Unknown"},
		{name: "When severity is unsupported it should error", in: "severe", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			got, err := normalizeTrustifySeverity(tt.in)
			if tt.wantErr {
				req.Error(err)
				return
			}
			req.NoError(err)
			req.Equal(tt.want, got)
		})
	}
}

func TestTrustifyScanner_ensureClient_ConcurrentInitBuildsOnce(t *testing.T) {
	req := require.New(t)

	var calls int32
	orig := newVulnerabilityClient
	newVulnerabilityClient = func(_ context.Context, _ *config.TrustifyConfig) (VulnerabilityClient, error) {
		atomic.AddInt32(&calls, 1)
		// Stateless fake: no shared mutable state, safe for concurrent scans.
		return &stubClient{}, nil
	}
	t.Cleanup(func() { newVulnerabilityClient = orig })

	s := &trustifyScanner{cfg: &config.TrustifyConfig{Endpoint: "https://trustify.example"}}

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{{Digest: "sha256:aaaa"}})
			req.NoError(err)
		}()
	}
	wg.Wait()

	// Lazy construction must happen exactly once across concurrent scans.
	req.Equal(int32(1), atomic.LoadInt32(&calls))
}

func TestNewScanner_NilConfigReturnsNil(t *testing.T) {
	req := require.New(t)
	s, err := NewScanner(nil)
	req.NoError(err)
	req.Nil(s)
}

func TestNewScanner_ValidConfigReturnsScanner(t *testing.T) {
	req := require.New(t)
	s, err := NewScanner(&config.TrustifyConfig{Endpoint: "https://trustify.example"})
	req.NoError(err)
	req.NotNil(s)
	// The client is built lazily, so no client exists until the first scan.
	req.Nil(s.(*trustifyScanner).client)
}

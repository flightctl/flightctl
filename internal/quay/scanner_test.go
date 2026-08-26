package quay

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestScanner builds a Quay scanner against endpoint with the given
// concurrency limit and a debug-level captured logger. It lowers the client's
// retry backoff so retry-driven paths (5xx exhaustion, timeouts) stay fast.
func newTestScanner(t *testing.T, endpoint string, maxConcurrent int) (*quayScanner, *logtest.Hook) {
	t.Helper()
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	cfg := &config.QuayConfig{Endpoint: endpoint, Token: "test-token", MaxConcurrentRequests: maxConcurrent}
	s, err := NewScanner(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, s)
	qs := s.(*quayScanner)
	qs.client.backoffBase = time.Millisecond
	return qs, hook
}

// findingByCVE returns the single finding with the given CVE ID, failing if it
// is absent or duplicated.
func findingByCVE(t *testing.T, findings []vulnerability.Finding, cve string) vulnerability.Finding {
	t.Helper()
	var out []vulnerability.Finding
	for _, f := range findings {
		if f.CveID == cve {
			out = append(out, f)
		}
	}
	require.Lenf(t, out, 1, "expected exactly one finding for %s", cve)
	return out[0]
}

func TestExtractCVEIDs(t *testing.T) {
	tests := []struct {
		name string
		vuln Vulnerability
		want []string
	}{
		{
			name: "single CVE in Link",
			vuln: Vulnerability{Name: "RHSA-2024:1234", Link: "https://access.redhat.com/security/cve/CVE-2021-44228"},
			want: []string{"CVE-2021-44228"},
		},
		{
			name: "multiple CVEs in Link deduplicated and ordered",
			vuln: Vulnerability{Name: "RHSA-2024:1234", Link: "cve/CVE-2021-44228 cve/CVE-2021-45046 cve/CVE-2021-44228"},
			want: []string{"CVE-2021-44228", "CVE-2021-45046"},
		},
		{
			name: "no CVE in Link falls back to Name (Debian style)",
			vuln: Vulnerability{Name: "CVE-2019-12345", Link: "https://security-tracker.debian.org/tracker/DSA-4444-1"},
			want: []string{"CVE-2019-12345"},
		},
		{
			name: "no CVE in Link, Name is the CVE",
			vuln: Vulnerability{Name: "CVE-2020-0001", Link: "https://example.com/advisory"},
			want: []string{"CVE-2020-0001"},
		},
		{
			name: "seven-digit CVE number",
			vuln: Vulnerability{Link: "https://nvd.nist.gov/vuln/detail/CVE-2023-1234567"},
			want: []string{"CVE-2023-1234567"},
		},
		{
			name: "no extractable CVE",
			vuln: Vulnerability{Name: "RHSA-2024:1234", Link: "https://access.redhat.com/errata/RHSA-2024:1234"},
			want: nil,
		},
		{
			name: "empty vulnerability",
			vuln: Vulnerability{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractCVEIDs(tt.vuln))
		})
	}
}

func TestMapSeverity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Critical", "Critical"},
		{"Defcon1", "Critical"},
		{"High", "High"},
		{"Medium", "Medium"},
		{"Low", "Low"},
		{"Negligible", "None"},
		{"Unknown", "Unknown"},
		{"", "Unknown"},
		{"SomethingNew", "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, mapSeverity(tt.in))
		})
	}
}

func TestBuildIssuer(t *testing.T) {
	t.Run("mapped prefixes", func(t *testing.T) {
		cases := map[string]string{
			"rhel:8":       "Red Hat",
			"centos:7":     "Red Hat",
			"debian:11":    "Debian",
			"ubuntu:22.04": "Ubuntu",
			"alpine:3.18":  "Alpine",
			"amzn:2":       "Amazon",
			"oracle:8":     "Oracle",
		}
		for ns, wantName := range cases {
			iss := buildIssuer(ns)
			require.NotNil(t, iss)
			assert.Equal(t, wantName, iss.Name)
			assert.Nil(t, iss.CpeKey)
			assert.Nil(t, iss.Website)
			wantID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(ns)).String()
			assert.Equal(t, wantID, iss.ID)
		}
	})

	t.Run("unknown prefix falls back to verbatim namespace", func(t *testing.T) {
		iss := buildIssuer("suse:15")
		require.NotNil(t, iss)
		assert.Equal(t, "suse:15", iss.Name)
	})

	t.Run("namespace without colon", func(t *testing.T) {
		iss := buildIssuer("rhel")
		require.NotNil(t, iss)
		assert.Equal(t, "Red Hat", iss.Name)
	})

	t.Run("stable UUID across calls", func(t *testing.T) {
		assert.Equal(t, buildIssuer("debian:11").ID, buildIssuer("debian:11").ID)
	})

	t.Run("empty namespace returns nil", func(t *testing.T) {
		assert.Nil(t, buildIssuer(""))
	})
}

func TestCVSSScore(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name string
		meta Metadata
		want *float64
	}{
		{
			name: "CVSSv3 preferred",
			meta: Metadata{NVD: NVD{CVSSv3: CVSS{Score: 9.8, Vectors: "AV:N"}, CVSSv2: CVSS{Score: 7.5, Vectors: "AV:N"}}},
			want: f(9.8),
		},
		{
			name: "fallback to CVSSv2 when v3 absent",
			meta: Metadata{NVD: NVD{CVSSv2: CVSS{Score: 7.5, Vectors: "AV:N"}}},
			want: f(7.5),
		},
		{
			name: "zero score with vector is present, not absent",
			meta: Metadata{NVD: NVD{CVSSv3: CVSS{Score: 0, Vectors: "AV:N"}}},
			want: f(0),
		},
		{
			name: "zero-value v3 falls through to scored v2",
			meta: Metadata{NVD: NVD{CVSSv2: CVSS{Score: 5.0, Vectors: "AV:L"}}},
			want: f(5.0),
		},
		{
			name: "no enrichment returns nil",
			meta: Metadata{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cvssScore(tt.meta)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}

func TestFindingsFromReport_Nil(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	assert.Nil(t, findingsFromReport("sha256:abc", nil, log))
	assert.Nil(t, findingsFromReport("sha256:abc", &Response{}, log))
	assert.Nil(t, findingsFromReport("sha256:abc", &Response{Data: &Data{}}, log))
}

func TestFindingsFromReport_FieldMapping(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Name: "openssl",
			Vulnerabilities: []Vulnerability{{
				Name:          "RHSA-2024:1234",
				NamespaceName: "rhel:8",
				Description:   "an openssl flaw",
				Link:          "https://access.redhat.com/security/cve/CVE-2021-44228",
				Severity:      "Defcon1",
				Metadata:      Metadata{NVD: NVD{CVSSv3: CVSS{Score: 9.8, Vectors: "AV:N"}}},
			}},
		}}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 1)
	f := findings[0]

	assert.Equal(t, "CVE-2021-44228", f.CveID)
	assert.Equal(t, "sha256:img", f.ImageDigest)
	assert.Equal(t, "affected", f.Status)
	assert.Equal(t, "Critical", f.Severity)
	assert.Equal(t, "an openssl flaw", f.Description)
	require.NotNil(t, f.CvssScore)
	assert.Equal(t, 9.8, *f.CvssScore)
	require.NotNil(t, f.AdvisoryID)
	assert.Equal(t, "RHSA-2024:1234", *f.AdvisoryID)
	require.NotNil(t, f.Issuer)
	assert.Equal(t, "Red Hat", f.Issuer.Name)
	assert.Nil(t, f.PublishedAt)
}

func TestFindingsFromReport_AdvisoryFanOut(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Vulnerabilities: []Vulnerability{{
				Name:          "RHSA-2021:5678",
				NamespaceName: "rhel:8",
				Link:          "cve/CVE-2021-44228 cve/CVE-2021-45046",
				Severity:      "High",
			}},
		}}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 2)
	for _, cve := range []string{"CVE-2021-44228", "CVE-2021-45046"} {
		f := findingByCVE(t, findings, cve)
		require.NotNil(t, f.AdvisoryID)
		assert.Equal(t, "RHSA-2021:5678", *f.AdvisoryID)
		assert.Equal(t, "High", f.Severity)
	}
}

func TestFindingsFromReport_DebianNameIsCVE(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Vulnerabilities: []Vulnerability{{
				Name:          "CVE-2019-9999",
				NamespaceName: "debian:11",
				Link:          "https://security-tracker.debian.org/tracker/DSA-1",
				Severity:      "Low",
			}},
		}}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 1)
	assert.Equal(t, "CVE-2019-9999", findings[0].CveID)
	assert.Nil(t, findings[0].AdvisoryID, "a Name that is itself a CVE carries no advisory ID")
	assert.Equal(t, "Debian", findings[0].Issuer.Name)
}

func TestFindingsFromReport_SkipsNoCVEWithDebugLog(t *testing.T) {
	log, hook := logtest.NewNullLogger()
	log.SetLevel(logrus.DebugLevel)
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Vulnerabilities: []Vulnerability{{
				Name:     "RHSA-2024:0001",
				Link:     "https://access.redhat.com/errata/RHSA-2024:0001",
				Severity: "High",
			}},
		}}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	assert.Empty(t, findings)

	entry := hook.LastEntry()
	require.NotNil(t, entry, "expected a debug log for the skipped vulnerability")
	assert.Equal(t, logrus.DebugLevel, entry.Level)
	assert.Equal(t, "sha256:img", entry.Data["digest"])
	assert.Equal(t, "RHSA-2024:0001", entry.Data["name"])
}

func TestFindingsFromReport_DistinctCVEsAcrossFeatures(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{
			{Vulnerabilities: []Vulnerability{{Name: "CVE-2021-0001", NamespaceName: "rhel:8", Link: "cve/CVE-2021-0001", Severity: "High"}}},
			{Vulnerabilities: []Vulnerability{{Name: "CVE-2021-0002", NamespaceName: "debian:11", Link: "cve/CVE-2021-0002", Severity: "Low"}}},
		}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 2)
	assert.Equal(t, "High", findingByCVE(t, findings, "CVE-2021-0001").Severity)
	assert.Equal(t, "Low", findingByCVE(t, findings, "CVE-2021-0002").Severity)
}

func TestFindingsFromReport_DedupKeepsFirstOnConflict(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{
			{Vulnerabilities: []Vulnerability{{Name: "CVE-2021-44228", NamespaceName: "rhel:8", Link: "cve/CVE-2021-44228", Severity: "Critical"}}},
			{Vulnerabilities: []Vulnerability{{Name: "CVE-2021-44228", NamespaceName: "debian:11", Link: "cve/CVE-2021-44228", Severity: "Low"}}},
		}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 1)
	assert.Equal(t, "Critical", findings[0].Severity, "first occurrence of a CVE is authoritative")
	assert.Equal(t, "Red Hat", findings[0].Issuer.Name)
}

func TestFindingsFromReport_DeduplicatesByCVE(t *testing.T) {
	log, _ := logtest.NewNullLogger()
	report := &Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{
			{Vulnerabilities: []Vulnerability{{
				Name: "CVE-2021-44228", NamespaceName: "rhel:8", Link: "cve/CVE-2021-44228", Severity: "Critical",
			}}},
			{Vulnerabilities: []Vulnerability{{
				Name: "RHSA-2021:1", NamespaceName: "rhel:8", Link: "cve/CVE-2021-44228", Severity: "Critical",
			}}},
		}}},
	}

	findings := findingsFromReport("sha256:img", report, log)
	require.Len(t, findings, 1, "the same (digest, cve) must collapse to one finding")
	assert.Equal(t, "CVE-2021-44228", findings[0].CveID)
}

func TestAdvisoryID(t *testing.T) {
	s := func(v string) *string { return &v }
	tests := []struct {
		name string
		in   string
		want *string
	}{
		{"advisory name", "RHSA-2024:1234", s("RHSA-2024:1234")},
		{"debian advisory", "DSA-5000-1", s("DSA-5000-1")},
		{"name is a CVE returns nil", "CVE-2021-44228", nil},
		{"empty returns nil", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := advisoryID(tt.in)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}

func TestNewScanner_NilConfig(t *testing.T) {
	s, err := NewScanner(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, s, "a nil Quay config yields no scanner")
}

func TestNewScanner_InvalidEndpoint(t *testing.T) {
	s, err := NewScanner(&config.QuayConfig{Endpoint: ""}, nil)
	require.Error(t, err, "an unparseable endpoint must surface as a constructor error")
	assert.Nil(t, s)
}

func scannedResponse() Response {
	return Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Name:          "openssl",
			NamespaceName: "rhel:9",
			Vulnerabilities: []Vulnerability{{
				Name:     "RHSA-2024:1234",
				Link:     "https://access.redhat.com/security/cve/CVE-2024-0001",
				Severity: "High",
				Metadata: Metadata{NVD: NVD{CVSSv3: CVSS{Score: 9.8, Vectors: "AV:N"}}},
			}},
		}}}},
	}
}

func TestScanImages_Success(t *testing.T) {
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	s, err := NewScanner(&config.QuayConfig{Endpoint: srv.URL, Token: "test-token"}, nil)
	require.NoError(t, err)
	require.NotNil(t, s)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:abc", Image: hostOf(srv) + "/testorg/testrepo:latest"},
	})
	require.NoError(t, err)
	require.Contains(t, out, "sha256:abc")
	require.Len(t, out["sha256:abc"], 1)
	f := out["sha256:abc"][0]
	assert.Equal(t, "CVE-2024-0001", f.CveID)
	assert.Equal(t, "affected", f.Status)
	assert.Equal(t, "High", f.Severity)
}

func TestScanImages_SkipsImagesOnOtherRegistries(t *testing.T) {
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	s, err := NewScanner(&config.QuayConfig{Endpoint: srv.URL, Token: "test-token"}, nil)
	require.NoError(t, err)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:other", Image: "docker.io/library/nginx:latest"},
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "sha256:other", "images on other registries are filtered out")
}

func TestScanImages_MultipleImages(t *testing.T) {
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	s, err := NewScanner(&config.QuayConfig{Endpoint: srv.URL, Token: "test-token"}, nil)
	require.NoError(t, err)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:a", Image: hostOf(srv) + "/org/a:latest"},
		{Digest: "sha256:b", Image: hostOf(srv) + "/org/b:latest"},
	})
	require.NoError(t, err)
	assert.Contains(t, out, "sha256:a")
	assert.Contains(t, out, "sha256:b")
}

func TestScanImages_AllFail(t *testing.T) {
	// A malformed 200 body makes every image's decode fail. When all images
	// fail, ScanImages returns a top-level error and no partial map.
	srv := newMockQuayServer(t, &mockQuayServer{rawBody: "{not json"})
	s, _ := newTestScanner(t, srv.URL, 5)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:a", Image: hostOf(srv) + "/org/a:latest"},
		{Digest: "sha256:b", Image: hostOf(srv) + "/org/b:latest"},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "all 2 image scans failed")
	assert.Nil(t, out, "when every image fails the scan reports a top-level error")
}

func TestScanImages_MixedIsolation(t *testing.T) {
	// One image's repo returns a persistent 500 (fails after retries); the other
	// scans cleanly. The failure is isolated: the healthy image's findings are
	// returned, no top-level error is raised, and a quay_scan_failed event is
	// logged for the broken image.
	srv := newMockQuayServer(t, &mockQuayServer{
		response:     scannedResponse(),
		statusByPath: map[string]int{"org/bad": http.StatusInternalServerError},
	})
	s, hook := newTestScanner(t, srv.URL, 5)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:good", Image: hostOf(srv) + "/org/good:latest"},
		{Digest: "sha256:bad", Image: hostOf(srv) + "/org/bad:latest"},
	})
	require.NoError(t, err, "a single failing image must not abort the scan")
	assert.Contains(t, out, "sha256:good")
	assert.NotContains(t, out, "sha256:bad")
	require.True(t, hasEntryWithField(hook, "event", eventScanFailed))
	require.True(t, hasEntryWithField(hook, "digest", "sha256:bad"))
}

func TestScanImages_TimeoutIsolation(t *testing.T) {
	// One image's repo stalls past the per-request timeout; after retries it is
	// isolated as a per-image failure while the fast image still scans.
	srv := newMockQuayServer(t, &mockQuayServer{
		response:    scannedResponse(),
		delayByPath: map[string]time.Duration{"org/slow": 500 * time.Millisecond},
	})
	s, _ := newTestScanner(t, srv.URL, 5)
	s.client.httpClient.Timeout = 60 * time.Millisecond

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:fast", Image: hostOf(srv) + "/org/fast:latest"},
		{Digest: "sha256:slow", Image: hostOf(srv) + "/org/slow:latest"},
	})
	require.NoError(t, err, "a timing-out image is isolated, not a full abort")
	assert.Contains(t, out, "sha256:fast")
	assert.NotContains(t, out, "sha256:slow")
}

func TestScanImages_Unauthorized(t *testing.T) {
	// A 401 on any image is terminal: ScanImages returns ErrQuayAuth and no map.
	srv := newMockQuayServer(t, &mockQuayServer{httpStatus: http.StatusUnauthorized})
	s, hook := newTestScanner(t, srv.URL, 1)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:a", Image: hostOf(srv) + "/org/a:latest"},
		{Digest: "sha256:b", Image: hostOf(srv) + "/org/b:latest"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQuayAuth)
	assert.Nil(t, out, "a 401 aborts the scan without partial results")
	require.True(t, hasEntryWithField(hook, "event", eventAuthError))
}

func TestScanImages_Forbidden(t *testing.T) {
	// A 403 skips just the affected image (warn); the rest continue.
	srv := newMockQuayServer(t, &mockQuayServer{
		response:     scannedResponse(),
		statusByPath: map[string]int{"org/denied": http.StatusForbidden},
	})
	s, hook := newTestScanner(t, srv.URL, 5)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:ok", Image: hostOf(srv) + "/org/ok:latest"},
		{Digest: "sha256:denied", Image: hostOf(srv) + "/org/denied:latest"},
	})
	require.NoError(t, err, "a 403 skips one image; it is not a hard error")
	assert.Contains(t, out, "sha256:ok")
	assert.NotContains(t, out, "sha256:denied")
	require.True(t, hasEntryWithField(hook, "reason", reasonForbidden))
}

func TestScanImages_BoundedConcurrency(t *testing.T) {
	// With more images than the concurrency limit and each request artificially
	// delayed, the observed max in-flight requests must never exceed the limit.
	const limit = 3
	mock := &mockQuayServer{response: scannedResponse(), delay: 40 * time.Millisecond}
	srv := newMockQuayServer(t, mock)
	s, _ := newTestScanner(t, srv.URL, limit)

	var images []vulnerability.ImageRef
	for i := 0; i < 9; i++ {
		images = append(images, vulnerability.ImageRef{
			Digest: fmt.Sprintf("sha256:%d", i),
			Image:  fmt.Sprintf("%s/org/repo%d:latest", hostOf(srv), i),
		})
	}

	out, err := s.ScanImages(context.Background(), images)
	require.NoError(t, err)
	require.Len(t, out, len(images))
	assert.LessOrEqual(t, mock.maxConcurrent(), limit, "concurrency is bounded by the semaphore")
	assert.Greater(t, mock.maxConcurrent(), 1, "requests do run concurrently")
}

func TestScanImages_SyncSummaryCounts(t *testing.T) {
	// A mixed run emits exactly one quay_sync_summary carrying the aggregate
	// per-image counts: one scanned, one registry-skip, one error-skip (404).
	srv := newMockQuayServer(t, &mockQuayServer{
		response:     scannedResponse(),
		statusByPath: map[string]int{"org/missing": http.StatusNotFound},
	})
	s, hook := newTestScanner(t, srv.URL, 5)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:ok", Image: hostOf(srv) + "/org/ok:latest"},
		{Digest: "sha256:missing", Image: hostOf(srv) + "/org/missing:latest"},
		{Digest: "sha256:elsewhere", Image: "docker.io/library/nginx:latest"},
	})
	require.NoError(t, err)
	assert.Contains(t, out, "sha256:ok")

	require.Equal(t, 1, countEntriesWithField(hook, "event", eventSyncSummary),
		"the summary is emitted exactly once per cycle")
	var summary *logrus.Entry
	for _, e := range hook.AllEntries() {
		if e.Data["event"] == eventSyncSummary {
			summary = e
		}
	}
	require.NotNil(t, summary)
	assert.Equal(t, 3, summary.Data["total_images"])
	assert.Equal(t, 1, summary.Data["scanned"])
	assert.Equal(t, 1, summary.Data["skipped_registry"])
	assert.Equal(t, 1, summary.Data["skipped_error"])
	assert.Equal(t, 0, summary.Data["failed"])
}

func TestScanImages_ScanCompletedEvent(t *testing.T) {
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	s, hook := newTestScanner(t, srv.URL, 5)

	_, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"},
	})
	require.NoError(t, err)

	var completed *logrus.Entry
	for _, e := range hook.AllEntries() {
		if e.Data["event"] == eventScanCompleted {
			completed = e
		}
	}
	require.NotNil(t, completed, "a successful scan emits quay_scan_completed")
	assert.Equal(t, "sha256:abc", completed.Data["digest"])
	assert.Equal(t, 1, completed.Data["cve_count"])
	assert.Contains(t, completed.Data, "duration_ms")
}

func TestScanImages_ParentContextCancelled(t *testing.T) {
	// A cancelled parent context must surface as an error, never as a partial map
	// the caller would mistake for a complete scan (design §4.6 ctx.Done respect).
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	s, _ := newTestScanner(t, srv.URL, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := s.ScanImages(ctx, []vulnerability.ImageRef{
		{Digest: "sha256:a", Image: hostOf(srv) + "/org/a:latest"},
		{Digest: "sha256:b", Image: hostOf(srv) + "/org/b:latest"},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, out, "cancellation yields no partial results")
}

func TestScanImages_ScannedWithNoFindingsOmitsDigest(t *testing.T) {
	// A scanned report whose only vulnerability has no extractable CVE yields
	// no findings, and the digest is omitted from the returned map.
	resp := Response{
		Status: statusScanned,
		Data: &Data{Layer: &Layer{Features: []Feature{{
			Vulnerabilities: []Vulnerability{{
				Name: "RHSA-2024:9", Link: "https://access.redhat.com/errata/RHSA-2024:9", Severity: "High",
			}},
		}}}},
	}
	srv := newMockQuayServer(t, &mockQuayServer{response: resp})
	s, err := NewScanner(&config.QuayConfig{Endpoint: srv.URL, Token: "test-token"}, nil)
	require.NoError(t, err)

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:empty", Image: hostOf(srv) + "/org/repo:latest"},
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "sha256:empty")
}

func TestRegistry_ResolvesQuayBackend(t *testing.T) {
	srv := newMockQuayServer(t, &mockQuayServer{response: scannedResponse()})
	cfg := &config.VulnerabilityConfig{
		Backend: config.VulnerabilityBackendQuay,
		Quay:    &config.QuayConfig{Endpoint: srv.URL, Token: "test-token"},
	}
	s, err := vulnerability.NewScanner(cfg)
	require.NoError(t, err)
	require.NotNil(t, s, "backend \"quay\" must resolve via the init() registration")

	out, err := s.ScanImages(context.Background(), []vulnerability.ImageRef{
		{Digest: "sha256:abc", Image: hostOf(srv) + "/org/repo:latest"},
	})
	require.NoError(t, err)
	assert.Contains(t, out, "sha256:abc")
}

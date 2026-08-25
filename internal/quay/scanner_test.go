package quay

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

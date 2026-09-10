package config

import (
	"testing"
)

func TestApplyVulnerabilityReportingEnvVarOverrides_QuayCAFile(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_CA_FILE", "/etc/ssl/certs/quay-ca.crt")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
		t.Fatal("When quay env vars are set it should initialize VulnerabilityReporting.Quay")
	}
	if c.VulnerabilityReporting.Quay.CAFile != "/etc/ssl/certs/quay-ca.crt" {
		t.Errorf("Quay CAFile = %q, want /etc/ssl/certs/quay-ca.crt", c.VulnerabilityReporting.Quay.CAFile)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_QuaySkipTLSVerify(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{
			name:     "When set to true it should parse to true",
			envValue: "true",
			expected: true,
		},
		{
			name:     "When set to 1 it should parse to true",
			envValue: "1",
			expected: true,
		},
		{
			name:     "When set to false it should parse to false",
			envValue: "false",
			expected: false,
		},
		{
			name:     "When set to 0 it should parse to false",
			envValue: "0",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
			t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_SKIP_TLS_VERIFY", tt.envValue)

			c := &Config{}
			applyVulnerabilityReportingEnvVarOverrides(c)

			if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
				t.Fatal("When quay env vars are set it should initialize VulnerabilityReporting.Quay")
			}
			if c.VulnerabilityReporting.Quay.SkipTLSVerify != tt.expected {
				t.Errorf("Quay SkipTLSVerify = %v, want %v", c.VulnerabilityReporting.Quay.SkipTLSVerify, tt.expected)
			}
		})
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_QuaySkipTLSVerifyInvalid(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_SKIP_TLS_VERIFY", "invalid")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
		t.Fatal("When quay env vars are set it should initialize VulnerabilityReporting.Quay")
	}
	// Invalid values should be ignored, leaving the default false value
	if c.VulnerabilityReporting.Quay.SkipTLSVerify {
		t.Error("When skipTlsVerify is invalid it should be ignored, leaving default false")
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_TrustifyCAFile(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_ENDPOINT", "https://trustify.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_CA_FILE", "/etc/ssl/certs/trustify-ca.crt")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Trustify == nil {
		t.Fatal("When trustify env vars are set it should initialize VulnerabilityReporting.Trustify")
	}
	if c.VulnerabilityReporting.Trustify.CAFile != "/etc/ssl/certs/trustify-ca.crt" {
		t.Errorf("Trustify CAFile = %q, want /etc/ssl/certs/trustify-ca.crt", c.VulnerabilityReporting.Trustify.CAFile)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_TrustifySkipTLSVerify(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{
			name:     "When set to true it should parse to true",
			envValue: "true",
			expected: true,
		},
		{
			name:     "When set to 1 it should parse to true",
			envValue: "1",
			expected: true,
		},
		{
			name:     "When set to false it should parse to false",
			envValue: "false",
			expected: false,
		},
		{
			name:     "When set to 0 it should parse to false",
			envValue: "0",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_ENDPOINT", "https://trustify.example.com")
			t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_SKIP_TLS_VERIFY", tt.envValue)

			c := &Config{}
			applyVulnerabilityReportingEnvVarOverrides(c)

			if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Trustify == nil {
				t.Fatal("When trustify env vars are set it should initialize VulnerabilityReporting.Trustify")
			}
			if c.VulnerabilityReporting.Trustify.SkipTLSVerify != tt.expected {
				t.Errorf("Trustify SkipTLSVerify = %v, want %v", c.VulnerabilityReporting.Trustify.SkipTLSVerify, tt.expected)
			}
		})
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_TrustifySkipTLSVerifyInvalid(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_ENDPOINT", "https://trustify.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_TRUSTIFY_SKIP_TLS_VERIFY", "invalid")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Trustify == nil {
		t.Fatal("When trustify env vars are set it should initialize VulnerabilityReporting.Trustify")
	}
	// Invalid values should be ignored, leaving the default false value
	if c.VulnerabilityReporting.Trustify.SkipTLSVerify {
		t.Error("When skipTlsVerify is invalid it should be ignored, leaving default false")
	}
}

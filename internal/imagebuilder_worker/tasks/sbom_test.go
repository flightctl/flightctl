package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testConsumer(t *testing.T, cfg *config.Config) *Consumer {
	t.Helper()
	log := flightlog.InitLogs()
	return NewConsumer(nil, nil, nil, nil, nil, nil, nil, nil, cfg, log)
}

func TestConsumer_transformSBOM(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := logrus.New()
	log.SetOutput(testingWriter{t})

	minSBOM := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[{"type":"library","name":"acl","purl":"pkg:rpm/centos/acl@1.0?arch=x86_64&distro=centos-9&upstream=x"}]}`)

	t.Run("When PURL transform is disabled it should still enrich metadata and write sbom-transformed.json", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewDefault()
		disabled := false
		cfg.ImageBuilderWorker.SBOM.PurlTransform = &config.PurlTransformConfig{Enabled: &disabled}

		dir := t.TempDir()
		sbomPath := filepath.Join(dir, "sbom.json")
		require.NoError(t, os.WriteFile(sbomPath, minSBOM, 0600))

		c := testConsumer(t, cfg)
		outPath, err := c.transformSBOM(ctx, sbomPath, dir, "quay.io/test/image:v1", "sha256:abc123", log)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dir, "sbom-transformed.json"), outPath)
	})

	t.Run("When PURL transform is enabled it should write sbom-transformed.json", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewDefault()

		dir := t.TempDir()
		sbomPath := filepath.Join(dir, "sbom.json")
		require.NoError(t, os.WriteFile(sbomPath, minSBOM, 0600))

		c := testConsumer(t, cfg)
		outPath, err := c.transformSBOM(ctx, sbomPath, dir, "quay.io/test/image:v1", "sha256:abc123", log)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dir, "sbom-transformed.json"), outPath)

		transformed, err := os.ReadFile(outPath)
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, json.Unmarshal(transformed, &doc))
		comps := doc["components"].([]interface{})
		c0 := comps[0].(map[string]interface{})
		purlStr, ok := c0["purl"].(string)
		require.True(t, ok)
		require.Equal(t, "pkg:rpm/redhat/acl@1.0?arch=x86_64&distro=rhel-9", purlStr)
		require.NotContains(t, purlStr, "upstream")
	})

	t.Run("When the SBOM file is missing it should return an error", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewDefault()
		dir := t.TempDir()
		c := testConsumer(t, cfg)
		_, err := c.transformSBOM(ctx, filepath.Join(dir, "missing.json"), dir, "quay.io/test/image:v1", "sha256:abc123", log)
		require.Error(t, err)
	})
}

func TestConsumer_shouldRunSBOMPipeline_BackendCapability(t *testing.T) {
	t.Parallel()

	// sbomOnlyTrustifyUpload configures SBOM so the pipeline decision hinges
	// solely on the vulnerability backend's SBOM-upload capability: generation
	// enabled, registry push disabled (so it does not short-circuit to true),
	// and Trustify upload enabled.
	sbomOnlyTrustifyUpload := func() *config.SBOMConfig {
		return &config.SBOMConfig{Enabled: true, PushToRegistry: false, UploadToTrustify: true}
	}

	tests := []struct {
		name string
		vuln *config.VulnerabilityConfig
		want bool
	}{
		{
			name: "When the backend is trustify it should run the pipeline (trustify requires SBOM upload)",
			vuln: &config.VulnerabilityConfig{Enabled: true, Backend: config.VulnerabilityBackendTrustify, Trustify: &config.TrustifyConfig{}},
			want: true,
		},
		{
			name: "When the backend is empty but a trustify block is present it should run the pipeline",
			vuln: &config.VulnerabilityConfig{Enabled: true, Trustify: &config.TrustifyConfig{}},
			want: true,
		},
		{
			name: "When the backend is quay it should skip the pipeline (quay indexes natively)",
			vuln: &config.VulnerabilityConfig{Enabled: true, Backend: config.VulnerabilityBackendQuay},
			want: false,
		},
		{
			// Discriminates the capability check from a plain Trustify-block
			// nil-check: a lingering Trustify block must not force SBOM upload
			// once the backend is explicitly quay.
			name: "When the backend is quay it should skip even if a stale trustify block is present",
			vuln: &config.VulnerabilityConfig{Enabled: true, Backend: config.VulnerabilityBackendQuay, Trustify: &config.TrustifyConfig{}},
			want: false,
		},
		{
			name: "When the backend is empty with no trustify block it should skip the pipeline",
			vuln: &config.VulnerabilityConfig{Enabled: true},
			want: false,
		},
		{
			name: "When vulnerability reporting is disabled it should skip the pipeline",
			vuln: &config.VulnerabilityConfig{Enabled: false, Backend: config.VulnerabilityBackendTrustify, Trustify: &config.TrustifyConfig{}},
			want: false,
		},
		{
			name: "When vulnerability reporting is nil it should skip the pipeline",
			vuln: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.NewDefault()
			cfg.ImageBuilderWorker.SBOM = sbomOnlyTrustifyUpload()
			cfg.VulnerabilityReporting = tt.vuln

			c := testConsumer(t, cfg)
			require.Equal(t, tt.want, c.shouldRunSBOMPipeline())
		})
	}
}

// testingWriter sends log output to the test log (optional noise reduction).
type testingWriter struct{ t *testing.T }

func (w testingWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Logf("%s", p)
	return len(p), nil
}

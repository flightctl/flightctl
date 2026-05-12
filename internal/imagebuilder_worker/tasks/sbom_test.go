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
	return NewConsumer(nil, nil, nil, nil, nil, nil, cfg, log)
}

func TestConsumer_transformSBOM(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := logrus.New()
	log.SetOutput(testingWriter{t})

	minSBOM := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[{"type":"library","name":"acl","purl":"pkg:rpm/centos/acl@1.0?arch=x86_64&distro=centos-9&upstream=x"}]}`)

	t.Run("When PURL transform is disabled it should return the original path", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewDefault()
		disabled := false
		cfg.ImageBuilderWorker.SBOM.PurlTransform = &config.PurlTransformConfig{Enabled: &disabled}

		dir := t.TempDir()
		sbomPath := filepath.Join(dir, "sbom.json")
		require.NoError(t, os.WriteFile(sbomPath, minSBOM, 0600))

		c := testConsumer(t, cfg)
		outPath, err := c.transformSBOM(ctx, sbomPath, dir, log)
		require.NoError(t, err)
		require.Equal(t, sbomPath, outPath)
	})

	t.Run("When PURL transform is enabled it should write sbom-transformed.json", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewDefault()

		dir := t.TempDir()
		sbomPath := filepath.Join(dir, "sbom.json")
		require.NoError(t, os.WriteFile(sbomPath, minSBOM, 0600))

		c := testConsumer(t, cfg)
		outPath, err := c.transformSBOM(ctx, sbomPath, dir, log)
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
		_, err := c.transformSBOM(ctx, filepath.Join(dir, "missing.json"), dir, log)
		require.Error(t, err)
	})
}

// testingWriter sends log output to the test log (optional noise reduction).
type testingWriter struct{ t *testing.T }

func (w testingWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Logf("%s", p)
	return len(p), nil
}

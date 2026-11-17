package updatercatalog_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/stretchr/testify/require"
)

var extractionBudgetKeys = []string{"maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds"}

func extractionFixture() map[string]any {
	return map[string]any{
		"maxExtractedManifestBytes": int64(65536),
		"maxExtractedFiles":         int64(64),
		"maxExtractedFileBytes":     int64(67108864),
		"maxExtractedTotalBytes":    int64(134217728),
		"extractionScratchBytes":    int64(134283264),
		"maxExtractionSeconds":      int64(300),
	}
}

func addExtraction(c map[string]any, extraction map[string]any, keys ...string) {
	r := release(c)
	r["extraction"] = extraction
	budget := append([]string(nil), r["budgetKeySet"].([]string)...)
	r["budgetKeySet"] = append(budget, keys...)
}

func TestCatalogExtractionBudgets(t *testing.T) {
	t.Run("When a release declares a valid extraction object with listed keys it should be accepted and carried in its descriptor", func(t *testing.T) {
		require := require.New(t)
		c := fixture(t)
		addExtraction(c, extractionFixture(), extractionBudgetKeys...)
		cfg, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(err)
		require.NotNil(cfg.Releases[0].Extraction)
		require.Equal(int64(64), cfg.Releases[0].Extraction.MaxExtractedFiles)
		d, err := cfg.Releases[0].Descriptor()
		require.NoError(err)
		var descriptor map[string]any
		require.NoError(json.Unmarshal(d.DescriptorJSON, &descriptor))
		require.Contains(descriptor, "extraction")
		require.Equal(float64(300), descriptor["extraction"].(map[string]any)["maxExtractionSeconds"])
	})
	t.Run("When a release has no extraction object it should be accepted unchanged", func(t *testing.T) {
		require := require.New(t)
		cfg, err := updatercatalog.ParseAdministratorConfig(encode(t, fixture(t)))
		require.NoError(err)
		require.Nil(cfg.Releases[0].Extraction)
		d, err := cfg.Releases[0].Descriptor()
		require.NoError(err)
		require.NotContains(string(d.DescriptorJSON), "extraction")
	})
	testCases := []struct {
		name   string
		mutate func(c map[string]any)
	}{
		{
			name: "When an extraction key is missing from the budget key evidence it should be rejected",
			mutate: func(c map[string]any) {
				addExtraction(c, extractionFixture(), extractionBudgetKeys[:5]...)
			},
		},
		{
			name: "When the budget key evidence names an extraction key without the object it should be rejected",
			mutate: func(c map[string]any) {
				r := release(c)
				r["budgetKeySet"] = append(append([]string(nil), r["budgetKeySet"].([]string)...), "maxExtractedFiles")
			},
		},
		{
			name: "When an extraction budget is zero it should be rejected",
			mutate: func(c map[string]any) {
				e := extractionFixture()
				e["maxExtractedFiles"] = int64(0)
				addExtraction(c, e, extractionBudgetKeys...)
			},
		},
		{
			name: "When one file may exceed the total budget it should be rejected",
			mutate: func(c map[string]any) {
				e := extractionFixture()
				e["maxExtractedFileBytes"] = int64(134217729)
				addExtraction(c, e, extractionBudgetKeys...)
			},
		},
		{
			name: "When scratch cannot hold the files and the manifest it should be rejected",
			mutate: func(c map[string]any) {
				e := extractionFixture()
				e["extractionScratchBytes"] = int64(134217728)
				addExtraction(c, e, extractionBudgetKeys...)
			},
		},
		{
			name: "When the extraction time exceeds one day it should be rejected",
			mutate: func(c map[string]any) {
				e := extractionFixture()
				e["maxExtractionSeconds"] = int64(86401)
				addExtraction(c, e, extractionBudgetKeys...)
			},
		},
		{
			name: "When the extraction object has an unknown field it should be rejected",
			mutate: func(c map[string]any) {
				e := extractionFixture()
				e["maxExtractedDirectories"] = int64(1)
				addExtraction(c, e, extractionBudgetKeys...)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			tc.mutate(c)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
}

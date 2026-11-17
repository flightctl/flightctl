package firmware_test

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/stretchr/testify/require"
)

var extractionBudgetKeys = []string{"maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds"}

func testExtraction() catalog.ExtractionLimits {
	return catalog.ExtractionLimits{
		MaxExtractedManifestBytes: 65536,
		MaxExtractedFiles:         64,
		MaxExtractedFileBytes:     64 << 20,
		MaxExtractedTotalBytes:    128 << 20,
		ExtractionScratchBytes:    128<<20 + 65536,
		MaxExtractionSeconds:      300,
	}
}

func asPolicyLimits(l catalog.ExtractionLimits) policy.ExtractionLimits {
	return policy.ExtractionLimits{
		MaxExtractedManifestBytes: l.MaxExtractedManifestBytes,
		MaxExtractedFiles:         l.MaxExtractedFiles,
		MaxExtractedFileBytes:     l.MaxExtractedFileBytes,
		MaxExtractedTotalBytes:    l.MaxExtractedTotalBytes,
		ExtractionScratchBytes:    l.ExtractionScratchBytes,
		MaxExtractionSeconds:      l.MaxExtractionSeconds,
	}
}

// withExtractionBudget returns the release with the given extraction object
// and its keys added to the budget key set.
func withExtractionBudget(r catalog.ReleaseBinding, l catalog.ExtractionLimits) catalog.ReleaseBinding {
	r.Extraction = &l
	r.BudgetKeySet = append(append([]string(nil), r.BudgetKeySet...), extractionBudgetKeys...)
	return r
}

func TestMaxTreeNodes(t *testing.T) {
	testCases := []struct {
		name    string
		files   int
		depth   int
		want    int
		wantErr bool
	}{
		{name: "When the tree has the maximum files and depth it should count 61443 nodes", files: 4096, depth: 16, want: 61443},
		{name: "When the tree has one file directly in payload it should count 4 nodes", files: 1, depth: 2, want: 4},
		{name: "When there are no files it should reject the input", files: 0, depth: 16, wantErr: true},
		{name: "When there are more files than the ceiling it should reject the input", files: 4097, depth: 16, wantErr: true},
		{name: "When the depth is below two it should reject the input", files: 4096, depth: 1, wantErr: true},
		{name: "When the depth is above the ceiling it should reject the input", files: 4096, depth: 17, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := firmware.MaxTreeNodes(tc.files, tc.depth)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, n)
		})
	}
}

func TestDefaultExtractionCeilings(t *testing.T) {
	require.Equal(t, policy.ExtractionLimits{
		MaxExtractedManifestBytes: 1 << 20,
		MaxExtractedFiles:         4096,
		MaxExtractedFileBytes:     1 << 30,
		MaxExtractedTotalBytes:    2 << 30,
		ExtractionScratchBytes:    3 << 30,
		MaxExtractionSeconds:      1800,
	}, firmware.DefaultExtractionCeilings())
	require.Equal(t, 16, firmware.MaxPayloadDepth)
}

func TestFetchBindsAuthenticatedExtractionLimits(t *testing.T) {
	t.Run("When the descriptor carries an extraction object both the fresh and the cached package expose it", func(t *testing.T) {
		require := require.New(t)
		h := newFetchHarness(t)
		h.approve(withExtractionBudget(h.release, testExtraction()))
		h.setManifest(manifestFor(h.release))
		fetcher := h.fetcher()
		fresh, err := fetcher.Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		require.False(fresh.Cached)
		limits, ok := fresh.ExtractionLimits()
		require.True(ok)
		require.Equal(asPolicyLimits(testExtraction()), limits)

		h.podman.existsResult = 0
		cached, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		require.True(cached.Cached)
		limits, ok = cached.ExtractionLimits()
		require.True(ok)
		require.Equal(asPolicyLimits(testExtraction()), limits)
	})
	t.Run("When the verified record is edited the cached package still takes its limits from the descriptor", func(t *testing.T) {
		require := require.New(t)
		h := newFetchHarness(t)
		h.approve(withExtractionBudget(h.release, testExtraction()))
		h.setManifest(manifestFor(h.release))
		_, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		data, err := os.ReadFile(h.recordPath())
		require.NoError(err)
		require.NotContains(string(data), "extraction", "the record schema carries no extraction budget")
		h.podman.existsResult = 0
		cached, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		limits, ok := cached.ExtractionLimits()
		require.True(ok)
		require.Equal(asPolicyLimits(testExtraction()), limits)
	})
	t.Run("When the descriptor has no extraction object the package exposes none", func(t *testing.T) {
		require := require.New(t)
		h := newFetchHarness(t)
		fresh, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		_, ok := fresh.ExtractionLimits()
		require.False(ok)
		h.podman.existsResult = 0
		cached, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(err)
		require.True(cached.Cached)
		_, ok = cached.ExtractionLimits()
		require.False(ok)
	})
}

func TestEffectiveExtractionLimits(t *testing.T) {
	fetchWith := func(t *testing.T, r func(catalog.ReleaseBinding) catalog.ReleaseBinding) *firmware.VerifiedPackage {
		t.Helper()
		h := newFetchHarness(t)
		h.approve(r(h.release))
		h.setManifest(manifestFor(h.release))
		pkg, err := h.fetcher().Fetch(context.Background(), h.release.Name, h.release.Version)
		require.NoError(t, err)
		return pkg
	}
	withBudget := func(r catalog.ReleaseBinding) catalog.ReleaseBinding {
		return withExtractionBudget(r, testExtraction())
	}

	t.Run("When the authenticated budget is within the ceilings it should return it", func(t *testing.T) {
		require := require.New(t)
		pkg := fetchWith(t, withBudget)
		limits, err := firmware.EffectiveExtractionLimits(pkg, firmware.DefaultExtractionCeilings())
		require.NoError(err)
		require.Equal(asPolicyLimits(testExtraction()), limits)
	})
	t.Run("When the release declares no extraction budget it should refuse extraction", func(t *testing.T) {
		require := require.New(t)
		pkg := fetchWith(t, func(r catalog.ReleaseBinding) catalog.ReleaseBinding { return r })
		_, err := firmware.EffectiveExtractionLimits(pkg, firmware.DefaultExtractionCeilings())
		require.ErrorIs(err, firmware.ErrNoExtractionBudget)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		var fetchErr *firmware.FetchError
		require.ErrorAs(err, &fetchErr)
		require.Empty(fetchErr.ExceededBudget)
	})
	t.Run("When the package was not produced by a fetch it should refuse extraction", func(t *testing.T) {
		require := require.New(t)
		_, err := firmware.EffectiveExtractionLimits(&firmware.VerifiedPackage{Name: "n", Version: "v"}, firmware.DefaultExtractionCeilings())
		require.ErrorIs(err, firmware.ErrNoExtractionBudget)
		_, err = firmware.EffectiveExtractionLimits(nil, firmware.DefaultExtractionCeilings())
		require.Error(err)
	})
	t.Run("When the package identity no longer matches its binding it should refuse extraction", func(t *testing.T) {
		require := require.New(t)
		pkg := fetchWith(t, withBudget)
		pkg.DescriptorDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		_, err := firmware.EffectiveExtractionLimits(pkg, firmware.DefaultExtractionCeilings())
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.NotErrorIs(err, firmware.ErrNoExtractionBudget)
	})
	for _, key := range extractionBudgetKeys {
		t.Run("When the budget "+key+" exceeds the agent ceiling it should reject the release without clamping", func(t *testing.T) {
			require := require.New(t)
			pkg := fetchWith(t, withBudget)
			ceilings := firmware.DefaultExtractionCeilings()
			lowered := asPolicyLimits(testExtraction())
			values := lowered.Values()
			lower := map[string]*int64{
				"maxExtractedManifestBytes": &ceilings.MaxExtractedManifestBytes,
				"maxExtractedFiles":         &ceilings.MaxExtractedFiles,
				"maxExtractedFileBytes":     &ceilings.MaxExtractedFileBytes,
				"maxExtractedTotalBytes":    &ceilings.MaxExtractedTotalBytes,
				"extractionScratchBytes":    &ceilings.ExtractionScratchBytes,
				"maxExtractionSeconds":      &ceilings.MaxExtractionSeconds,
			}
			*lower[key] = values[key] - 1
			_, err := firmware.EffectiveExtractionLimits(pkg, ceilings)
			requireFetchError(t, err, firmware.ReasonFirmwareNotSupported, false)
			require.ErrorContains(err, key)
			var fetchErr *firmware.FetchError
			require.ErrorAs(err, &fetchErr)
			require.Empty(fetchErr.ExceededBudget)
		})
	}
}

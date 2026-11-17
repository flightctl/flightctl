package firmwarepolicy_test

import (
	"encoding/json"
	"math"
	"sort"
	"testing"

	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
)

var extractionKeys = []string{"maxExtractedManifestBytes", "maxExtractedFiles", "maxExtractedFileBytes", "maxExtractedTotalBytes", "extractionScratchBytes", "maxExtractionSeconds"}

func validExtraction() firmwarepolicy.ExtractionLimits {
	return firmwarepolicy.ExtractionLimits{
		MaxExtractedManifestBytes: 65536,
		MaxExtractedFiles:         64,
		MaxExtractedFileBytes:     64 << 20,
		MaxExtractedTotalBytes:    128 << 20,
		ExtractionScratchBytes:    128<<20 + 65536,
		MaxExtractionSeconds:      300,
	}
}

func TestExtractionLimitsValidate(t *testing.T) {
	testCases := []struct {
		name    string
		mutate  func(l *firmwarepolicy.ExtractionLimits)
		wantErr bool
	}{
		{name: "When every budget is positive and consistent it should accept them", mutate: func(*firmwarepolicy.ExtractionLimits) {}},
		{name: "When the extraction time equals one day it should accept it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractionSeconds = 86400 }},
		{name: "When the manifest budget is zero it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractedManifestBytes = 0 }, wantErr: true},
		{name: "When the file count is negative it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractedFiles = -1 }, wantErr: true},
		{name: "When the file size budget is zero it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractedFileBytes = 0 }, wantErr: true},
		{name: "When the total budget is zero it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractedTotalBytes = 0 }, wantErr: true},
		{name: "When the scratch budget is zero it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.ExtractionScratchBytes = 0 }, wantErr: true},
		{name: "When the extraction time is zero it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractionSeconds = 0 }, wantErr: true},
		{name: "When one file may exceed the total budget it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractedFileBytes = l.MaxExtractedTotalBytes + 1 }, wantErr: true},
		{name: "When scratch cannot hold the files and the manifest it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) {
			l.ExtractionScratchBytes = l.MaxExtractedTotalBytes + l.MaxExtractedManifestBytes - 1
		}, wantErr: true},
		{name: "When the extraction time exceeds one day it should reject it", mutate: func(l *firmwarepolicy.ExtractionLimits) { l.MaxExtractionSeconds = 86401 }, wantErr: true},
		{name: "When the total and manifest budgets overflow it should reject them", mutate: func(l *firmwarepolicy.ExtractionLimits) {
			l.MaxExtractedTotalBytes = math.MaxInt64
			l.MaxExtractedFileBytes = math.MaxInt64
			l.ExtractionScratchBytes = math.MaxInt64
		}, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			l := validExtraction()
			tc.mutate(&l)
			err := l.Validate()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestExtractionLimitsValuesUseTheBudgetKeys(t *testing.T) {
	require := require.New(t)
	l := validExtraction()
	data, err := json.Marshal(l)
	require.NoError(err)
	var asJSON map[string]int64
	require.NoError(json.Unmarshal(data, &asJSON))
	require.Equal(asJSON, l.Values())
	keys := make([]string, 0, len(asJSON))
	for k := range asJSON {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := append([]string(nil), extractionKeys...)
	sort.Strings(want)
	require.Equal(want, keys)
}

func withExtraction(m map[string]any, extraction map[string]any, keys ...string) {
	m["extraction"] = extraction
	budget := m["budgetKeySet"].([]any)
	for _, k := range keys {
		budget = append(budget, k)
	}
	m["budgetKeySet"] = budget
}

func extractionObject(t *testing.T, l firmwarepolicy.ExtractionLimits) map[string]any {
	t.Helper()
	data, err := json.Marshal(l)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func TestDecodeDescriptorExtraction(t *testing.T) {
	t.Run("When every extraction key is a listed budget key it should decode the authenticated object", func(t *testing.T) {
		require := require.New(t)
		b := mutatedBinding(t, func(m map[string]any) {
			withExtraction(m, extractionObject(t, validExtraction()), extractionKeys...)
		})
		d, err := firmwarepolicy.DecodeDescriptor(b)
		require.NoError(err)
		require.NotNil(d.Extraction)
		require.Equal(validExtraction(), *d.Extraction)
	})
	t.Run("When the descriptor has no extraction object it should decode as before", func(t *testing.T) {
		require := require.New(t)
		d, err := firmwarepolicy.DecodeDescriptor(mutatedBinding(t, func(map[string]any) {}))
		require.NoError(err)
		require.Nil(d.Extraction)
	})
	testCases := []struct {
		name   string
		mutate func(t *testing.T, m map[string]any)
	}{
		{
			name: "When an extraction key is not a listed budget key it should reject the descriptor",
			mutate: func(t *testing.T, m map[string]any) {
				withExtraction(m, extractionObject(t, validExtraction()), extractionKeys[1:]...)
			},
		},
		{
			name: "When the extraction object is inconsistent it should reject the descriptor",
			mutate: func(t *testing.T, m map[string]any) {
				l := validExtraction()
				l.ExtractionScratchBytes = l.MaxExtractedTotalBytes
				withExtraction(m, extractionObject(t, l), extractionKeys...)
			},
		},
		{
			name: "When the extraction object has an unknown field it should reject the descriptor",
			mutate: func(t *testing.T, m map[string]any) {
				obj := extractionObject(t, validExtraction())
				obj["maxExtractedDirectories"] = 1
				withExtraction(m, obj, extractionKeys...)
			},
		},
		{
			name: "When an extraction field name differs in case it should reject the descriptor",
			mutate: func(t *testing.T, m map[string]any) {
				obj := extractionObject(t, validExtraction())
				obj["MaxExtractedFiles"] = obj["maxExtractedFiles"]
				delete(obj, "maxExtractedFiles")
				withExtraction(m, obj, extractionKeys...)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := firmwarepolicy.DecodeDescriptor(mutatedBinding(t, func(m map[string]any) { tc.mutate(t, m) }))
			require.Error(t, err)
		})
	}
}

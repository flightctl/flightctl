package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadFixture reads a file from the testdata directory.
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(data)
}

// makeRun is a helper that builds a rawRunData with the given specs and N
// additional passing specs as padding to avoid tripping the infra instability
// threshold unless the test deliberately wants to do that.
func makeRun(id int64, url, date, conclusion string, extraPassing int, specs map[string]rawSpecResult) rawRunData {
	merged := make(map[string]rawSpecResult, len(specs)+extraPassing)
	for k, v := range specs {
		merged[k] = v
	}
	for i := 0; i < extraPassing; i++ {
		merged[fmt.Sprintf("Pad %s %d", date, i)] = rawSpecResult{Passed: true, DurationSec: 0.1}
	}
	return rawRunData{
		RunID:      id,
		RunURL:     url,
		Date:       date,
		Conclusion: conclusion,
		Specs:      merged,
	}
}

// --------------------------------------------------------------------------
// specNameFromCase
// --------------------------------------------------------------------------

// TestSpecNameFromCase covers both Ginkgo and standard Go test name extraction.
func TestSpecNameFromCase(t *testing.T) {
	tests := []struct {
		name      string
		tc        junitTestCase
		suiteName string
		want      string
	}{
		{
			name:  "When name has [It] prefix and labels it should strip both",
			tc:    junitTestCase{Name: "[It] Store Suite should create a device [12345, integration]", ClassName: "Store Suite"},
			want:  "Store Suite should create a device",
		},
		{
			name:  "When name has [It] prefix but no labels it should strip only the prefix",
			tc:    junitTestCase{Name: "[It] Simple spec", ClassName: "Suite"},
			want:  "Simple spec",
		},
		{
			name:      "When name is a standard Go test with classname it should return classname/TestName",
			tc:        junitTestCase{Name: "TestCreate", ClassName: "github.com/flightctl/flightctl/internal/store"},
			suiteName: "",
			want:      "internal/store/TestCreate",
		},
		{
			name:      "When name is a standard Go test with a subtest it should include the subtest",
			tc:        junitTestCase{Name: "TestCreate/with_valid_input", ClassName: "github.com/flightctl/flightctl/internal/store"},
			want:      "internal/store/TestCreate/with_valid_input",
		},
		{
			name:      "When classname is empty it should fall back to suiteName",
			tc:        junitTestCase{Name: "TestFoo", ClassName: ""},
			suiteName: "github.com/flightctl/flightctl/pkg/util",
			want:      "pkg/util/TestFoo",
		},
		{
			name:      "When classname is empty and suiteName is also empty it should return the raw name",
			tc:        junitTestCase{Name: "TestFoo", ClassName: ""},
			suiteName: "",
			want:      "TestFoo",
		},
		{
			name:  "When name contains a pkg path anchor it should strip the module prefix",
			tc:    junitTestCase{Name: "TestList", ClassName: "github.com/flightctl/flightctl/pkg/version"},
			want:  "pkg/version/TestList",
		},
		{
			name:  "When name contains a cmd path anchor it should strip the module prefix",
			tc:    junitTestCase{Name: "TestRun", ClassName: "github.com/flightctl/flightctl/cmd/flightctl"},
			want:  "cmd/flightctl/TestRun",
		},
		{
			name:  "When name contains an api path anchor it should strip the module prefix",
			tc:    junitTestCase{Name: "TestValidate", ClassName: "github.com/flightctl/flightctl/api/v1beta1"},
			want:  "api/v1beta1/TestValidate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, specNameFromCase(tt.tc, tt.suiteName))
		})
	}
}

// --------------------------------------------------------------------------
// aggregateRuns
// --------------------------------------------------------------------------

// TestAggregateRuns covers pass/fail accumulation, infra instability exclusion,
// and handling of runs with no JUnit output.
func TestAggregateRuns(t *testing.T) {
	t.Run("When a run has more than 30 percent of specs failing it should be excluded as infra instability", func(t *testing.T) {
		// 5 specs, 4 failing = 80% > threshold
		run := rawRunData{
			RunID: 1, RunURL: "u1", Date: "d", Conclusion: "failure",
			Specs: map[string]rawSpecResult{
				"Spec A": {Passed: false},
				"Spec B": {Passed: false},
				"Spec C": {Passed: false},
				"Spec D": {Passed: false},
				"Spec E": {Passed: true},
			},
		}
		agg := aggregateRuns([]rawRunData{run})
		require.Empty(t, agg.specs)
		require.Len(t, agg.infraRuns, 1)
	})

	t.Run("When a run has no specs it should be recorded in noJUnit", func(t *testing.T) {
		run := rawRunData{RunID: 2, RunURL: "u2", Date: "d", Conclusion: "failure", Specs: nil}
		agg := aggregateRuns([]rawRunData{run})
		require.Len(t, agg.noJUnit, 1)
		require.Empty(t, agg.specs)
	})

	t.Run("When a spec passes across runs it should accumulate pass counts", func(t *testing.T) {
		runs := []rawRunData{
			makeRun(1, "u1", "d1", "success", 9, map[string]rawSpecResult{"Spec A": {Passed: true}}),
			makeRun(2, "u2", "d2", "success", 9, map[string]rawSpecResult{"Spec A": {Passed: true}}),
		}
		agg := aggregateRuns(runs)
		require.Equal(t, 2, agg.specs["Spec A"].PassCount)
		require.Equal(t, 0, agg.specs["Spec A"].FailCount)
	})

	t.Run("When a spec fails in some runs it should accumulate fail counts and record the last URL", func(t *testing.T) {
		failURL := "https://github.com/org/repo/actions/runs/999"
		runs := []rawRunData{
			// Newest first (as collect returns them)
			makeRun(3, failURL, "d3", "success", 9, map[string]rawSpecResult{"Spec A": {Passed: false, FailureMsg: "timeout"}}),
			makeRun(2, "u2", "d2", "success", 9, map[string]rawSpecResult{"Spec A": {Passed: true}}),
		}
		agg := aggregateRuns(runs)
		require.Equal(t, 1, agg.specs["Spec A"].FailCount)
		require.Equal(t, failURL, agg.specs["Spec A"].LastFailedURL)
		require.Equal(t, "timeout", agg.specs["Spec A"].FailureMsg)
	})

	t.Run("When a spec is skipped it should increment skip count and not count as pass or fail", func(t *testing.T) {
		run := makeRun(1, "u1", "d1", "success", 9, map[string]rawSpecResult{"Spec A": {Skipped: true}})
		agg := aggregateRuns([]rawRunData{run})
		require.Equal(t, 1, agg.specs["Spec A"].SkipCount)
		require.Equal(t, 0, agg.specs["Spec A"].PassCount)
		require.Equal(t, 0, agg.specs["Spec A"].FailCount)
	})
}

// --------------------------------------------------------------------------
// computeFlakes
// --------------------------------------------------------------------------

// TestComputeFlakes covers classification, sorting, and metadata propagation.
func TestComputeFlakes(t *testing.T) {
	t.Run("When a spec fails some runs but also passes it should be classified as Flaky", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 10, FailCount: 4},
		}}
		entries, _, flakyCount, consistentCount, _ := computeFlakes(agg, 10)
		require.Len(t, entries, 1)
		require.Equal(t, "Flaky", entries[0].Class)
		require.Equal(t, 1, flakyCount)
		require.Equal(t, 0, consistentCount)
	})

	t.Run("When a spec never passes it should be classified as Consistently failing", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 0, FailCount: 8},
		}}
		entries, _, flakyCount, consistentCount, _ := computeFlakes(agg, 10)
		require.Len(t, entries, 1)
		require.Equal(t, "Consistently failing", entries[0].Class)
		require.Equal(t, 0, flakyCount)
		require.Equal(t, 1, consistentCount)
	})

	t.Run("When a spec never fails it should be counted as clean and not appear in flake entries", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 14, FailCount: 0},
		}}
		entries, totalSpecs, _, _, cleanCount := computeFlakes(agg, 10)
		require.Empty(t, entries)
		require.Equal(t, 1, totalSpecs)
		require.Equal(t, 1, cleanCount)
	})

	t.Run("When a spec is only ever skipped it should not be counted in totalSpecs", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {SkipCount: 5},
		}}
		_, totalSpecs, _, _, _ := computeFlakes(agg, 10)
		require.Equal(t, 0, totalSpecs)
	})

	t.Run("When multiple specs have failures they should be sorted by fail rate descending", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Low flake":  {PassCount: 9, FailCount: 1},
			"High flake": {PassCount: 2, FailCount: 8},
		}}
		entries, _, _, _, _ := computeFlakes(agg, 10)
		require.Len(t, entries, 2)
		require.Greater(t, entries[0].FailRate, entries[1].FailRate)
	})

	t.Run("When topN is smaller than flake count it should truncate to topN entries", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 1, FailCount: 9},
			"Spec B": {PassCount: 2, FailCount: 8},
			"Spec C": {PassCount: 3, FailCount: 7},
		}}
		entries, _, _, _, _ := computeFlakes(agg, 2)
		require.Len(t, entries, 2)
	})

	t.Run("When consistently failing entries are present they should appear before flaky ones", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Flaky":      {PassCount: 5, FailCount: 5},
			"Consistent": {PassCount: 0, FailCount: 5},
		}}
		entries, _, _, _, _ := computeFlakes(agg, 10)
		require.Len(t, entries, 2)
		require.Equal(t, "Consistently failing", entries[0].Class)
	})

	t.Run("When a flake entry is generated it should record the last failed run URL", func(t *testing.T) {
		failURL := "https://github.com/org/repo/actions/runs/42"
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 5, FailCount: 2, LastFailedURL: failURL},
		}}
		entries, _, _, _, _ := computeFlakes(agg, 10)
		require.Len(t, entries, 1)
		require.Equal(t, failURL, entries[0].LastRunURL)
	})

	t.Run("When a flake entry has a failure message it should propagate to the entry", func(t *testing.T) {
		agg := junitAgg{specs: map[string]specResult{
			"Spec A": {PassCount: 5, FailCount: 2, FailureMsg: "connection refused"},
		}}
		entries, _, _, _, _ := computeFlakes(agg, 10)
		require.Equal(t, "connection refused", entries[0].FailureMsg)
	})
}

// --------------------------------------------------------------------------
// computeTimings
// --------------------------------------------------------------------------

// TestComputeTimings verifies per-spec avg/stddev derived from raw run data.
func TestComputeTimings(t *testing.T) {
	t.Run("When two runs have the same spec it should average the durations", func(t *testing.T) {
		runs := []rawRunData{
			makeRun(1, "u1", "d1", "success", 0, map[string]rawSpecResult{"Spec A": {Passed: true, DurationSec: 100}}),
			makeRun(2, "u2", "d2", "success", 0, map[string]rawSpecResult{"Spec A": {Passed: true, DurationSec: 200}}),
		}
		timings := computeTimings(runs)
		require.InDelta(t, 150.0, timings["Spec A"].Avg, 0.001)
	})

	t.Run("When a spec has zero duration it should be excluded from timings", func(t *testing.T) {
		runs := []rawRunData{
			makeRun(1, "u1", "d1", "success", 0, map[string]rawSpecResult{"Spec A": {Passed: true, DurationSec: 0}}),
		}
		timings := computeTimings(runs)
		_, exists := timings["Spec A"]
		require.False(t, exists)
	})

	t.Run("When a spec is skipped it should be excluded from timings", func(t *testing.T) {
		runs := []rawRunData{
			makeRun(1, "u1", "d1", "success", 0, map[string]rawSpecResult{"Spec A": {Skipped: true, DurationSec: 10}}),
		}
		timings := computeTimings(runs)
		_, exists := timings["Spec A"]
		require.False(t, exists)
	})

	t.Run("When a run exceeds the infra instability threshold it should be excluded from timings", func(t *testing.T) {
		// 5 specs all failing = 100% > threshold
		specs := map[string]rawSpecResult{
			"Spec A": {Passed: false, DurationSec: 99},
			"Spec B": {Passed: false, DurationSec: 50},
			"Spec C": {Passed: false, DurationSec: 60},
			"Spec D": {Passed: false, DurationSec: 70},
			"Spec E": {Passed: false, DurationSec: 80},
		}
		run := rawRunData{RunID: 1, RunURL: "u1", Date: "d1", Conclusion: "failure", Specs: specs}
		timings := computeTimings([]rawRunData{run})
		require.Empty(t, timings)
	})
}

// --------------------------------------------------------------------------
// computeSlowest
// --------------------------------------------------------------------------

// TestComputeSlowest verifies descending order and top-N truncation.
func TestComputeSlowest(t *testing.T) {
	timings := map[string]specTiming{
		"fast":      {Avg: 10.0},
		"medium":    {Avg: 50.0},
		"slow":      {Avg: 200.0},
		"very slow": {Avg: 300.0},
	}

	t.Run("When specs are ranked by avg it should return the top N in descending order", func(t *testing.T) {
		result := computeSlowest(timings, 3)
		require.Len(t, result, 3)
		require.Equal(t, "very slow", result[0].Name)
		require.Equal(t, "slow", result[1].Name)
		require.Equal(t, "medium", result[2].Name)
	})

	t.Run("When topN exceeds available specs it should return all specs", func(t *testing.T) {
		result := computeSlowest(timings, 20)
		require.Len(t, result, 4)
	})

	t.Run("When topN is zero it should return all specs", func(t *testing.T) {
		result := computeSlowest(timings, 0)
		require.Len(t, result, 4)
	})
}

// --------------------------------------------------------------------------
// computeTrend
// --------------------------------------------------------------------------

// TestComputeTrend verifies per-run pass/fail/skip counts and ordering.
func TestComputeTrend(t *testing.T) {
	t.Run("When runs are in newest-first order trend should be reversed to oldest-first", func(t *testing.T) {
		runs := []rawRunData{
			{RunID: 3, Date: "2026-01-06", Conclusion: "success", Specs: map[string]rawSpecResult{"Spec A": {Passed: true}}},
			{RunID: 2, Date: "2026-01-05", Conclusion: "success", Specs: map[string]rawSpecResult{"Spec A": {Passed: true}}},
			{RunID: 1, Date: "2026-01-04", Conclusion: "success", Specs: map[string]rawSpecResult{"Spec A": {Passed: true}}},
		}
		trend := computeTrend(runs)
		require.Len(t, trend, 3)
		require.Equal(t, int64(1), trend[0].RunID)
		require.Equal(t, int64(3), trend[2].RunID)
	})

	t.Run("When a run has passed failed and skipped specs it should count each correctly", func(t *testing.T) {
		run := rawRunData{
			RunID: 1, Date: "d", Conclusion: "success",
			Specs: map[string]rawSpecResult{
				"Spec P1": {Passed: true},
				"Spec P2": {Passed: true},
				"Spec F1": {Passed: false},
				"Spec S1": {Skipped: true},
			},
		}
		trend := computeTrend([]rawRunData{run})
		require.Len(t, trend, 1)
		require.Equal(t, 2, trend[0].Passed)
		require.Equal(t, 1, trend[0].Failed)
		require.Equal(t, 1, trend[0].Skipped)
		require.Equal(t, 4, trend[0].Total)
	})

	t.Run("When a run has no specs the trend entry should have zero counts", func(t *testing.T) {
		run := rawRunData{RunID: 1, Date: "d", Conclusion: "failure", Specs: nil}
		trend := computeTrend([]rawRunData{run})
		require.Len(t, trend, 1)
		require.Equal(t, 0, trend[0].Total)
	})
}

// --------------------------------------------------------------------------
// avgStddev
// --------------------------------------------------------------------------

// TestAvgStddev covers the statistics helper.
func TestAvgStddev(t *testing.T) {
	tests := []struct {
		name       string
		input      []float64
		wantAvg    float64
		wantStddev float64
	}{
		{
			name:       "When step durations are provided it should compute avg and stddev correctly",
			input:      []float64{100, 200, 300},
			wantAvg:    200,
			wantStddev: 81.65,
		},
		{
			name:       "When a single value is provided stddev should be zero",
			input:      []float64{50},
			wantAvg:    50,
			wantStddev: 0,
		},
		{
			name:       "When slice is empty both should be zero",
			input:      []float64{},
			wantAvg:    0,
			wantStddev: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			avg, std := avgStddev(tt.input)
			require.InDelta(t, tt.wantAvg, avg, 0.01)
			require.InDelta(t, tt.wantStddev, std, 0.01)
		})
	}
}

// --------------------------------------------------------------------------
// formatDuration
// --------------------------------------------------------------------------

// TestFormatDuration covers the human-readable duration formatter.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		secs float64
		want string
	}{
		{"When duration is zero it should return 0s", 0, "0s"},
		{"When duration is sub-minute it should show seconds", 45, "45s"},
		{"When duration is exact minutes it should omit seconds", 120, "2m"},
		{"When duration has minutes and seconds it should show both", 125, "2m 5s"},
		{"When duration is negative it should return 0s", -10, "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, formatDuration(tt.secs))
		})
	}
}

// --------------------------------------------------------------------------
// truncateMsg
// --------------------------------------------------------------------------

// TestTruncateMsg covers message truncation and whitespace normalization.
func TestTruncateMsg(t *testing.T) {
	t.Run("When message is within the limit it should be returned unchanged", func(t *testing.T) {
		require.Equal(t, "short msg", truncateMsg("short msg", 200))
	})
	t.Run("When message exceeds the limit it should be truncated with ellipsis", func(t *testing.T) {
		buf := make([]byte, 300)
		for i := range buf {
			buf[i] = 'a'
		}
		result := truncateMsg(string(buf), 200)
		require.Len(t, []rune(result), 201)
		require.True(t, len(result) > 200)
	})
	t.Run("When message contains newlines they should be normalized to spaces", func(t *testing.T) {
		result := truncateMsg("line1\nline2\nline3", 200)
		require.Equal(t, "line1 line2 line3", result)
	})
}

// --------------------------------------------------------------------------
// renderHTML / renderSlackSummary / appendStepSummary (fixture-based)
// --------------------------------------------------------------------------

// TestRenderHTML exercises the full compute+render path using the testdata fixture.
func TestRenderHTML(t *testing.T) {
	var raw rawFile
	require.NoError(t, loadJSON("testdata/raw-junit.json", &raw))

	report := computeReport(raw, "Integration Test Health", 10)

	outPath := filepath.Join(t.TempDir(), "report.html")
	require.NoError(t, renderHTML(report, outPath))

	content, err := os.ReadFile(outPath)
	require.NoError(t, err)
	html := string(content)

	t.Run("When all sections have data it should produce a valid HTML file with all sections", func(t *testing.T) {
		require.Contains(t, html, `id="trend"`)
		require.Contains(t, html, `id="flakes"`)
		require.Contains(t, html, `id="slowest"`)
	})

	t.Run("When the HTML file is parsed it should contain SVG elements for the charts", func(t *testing.T) {
		require.Contains(t, html, "<svg ")
		require.Contains(t, html, "<rect ")
	})

	t.Run("When the title is set it should appear in the HTML", func(t *testing.T) {
		require.Contains(t, html, "Integration Test Health")
	})
}

// TestRenderSlackSummary verifies the JSON summary fields.
func TestRenderSlackSummary(t *testing.T) {
	var raw rawFile
	require.NoError(t, loadJSON("testdata/raw-junit.json", &raw))

	report := computeReport(raw, "Integration Test Health", 10)

	path := filepath.Join(t.TempDir(), "summary.json")
	require.NoError(t, renderSlackSummary(report, path))

	t.Run("When the JSON summary is written it should contain the required fields", func(t *testing.T) {
		var s slackSummary
		require.NoError(t, loadJSON(path, &s))
		require.Equal(t, "Integration Test Health", s.Title)
		require.GreaterOrEqual(t, s.AnalyzedRuns, 0)
		require.GreaterOrEqual(t, s.TotalSpecsTracked, 0)
		require.GreaterOrEqual(t, s.ConsistentlyFailing, 0)
		require.GreaterOrEqual(t, s.FlakyTests, 0)
		require.GreaterOrEqual(t, s.NeverFailed, 0)
		require.GreaterOrEqual(t, s.InfraInstabilityEvents, 0)
	})
}

// TestStepSummaryNoScriptTags verifies the step summary is plain HTML.
func TestStepSummaryNoScriptTags(t *testing.T) {
	var raw rawFile
	require.NoError(t, loadJSON("testdata/raw-junit.json", &raw))

	report := computeReport(raw, "Integration Test Health", 10)

	summaryPath := filepath.Join(t.TempDir(), "summary.html")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)

	require.NoError(t, appendStepSummary(report))

	content, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	html := string(content)

	t.Run("When the step summary is written it should contain only HTML with no script or svg tags", func(t *testing.T) {
		require.NotContains(t, html, "<script")
		require.NotContains(t, html, "<svg")
		require.Contains(t, html, "<table")
		require.Contains(t, html, "<h2>")
	})
}

// --------------------------------------------------------------------------
// computeReport (end-to-end)
// --------------------------------------------------------------------------

// TestComputeReportFromFixture verifies the end-to-end compute pipeline.
func TestComputeReportFromFixture(t *testing.T) {
	var raw rawFile
	require.NoError(t, loadJSON("testdata/raw-junit.json", &raw))

	report := computeReport(raw, "Integration Test Health", 10)

	t.Run("When fixture data is loaded it should produce non-empty report fields", func(t *testing.T) {
		require.Equal(t, "Integration Test Health", report.Title)
		require.Equal(t, raw.Meta.AnalyzedRuns, report.AnalyzedRuns)
		require.Greater(t, report.TotalAnalyzedSpecs, 0)
		require.NotEmpty(t, report.TrendEntries)
		require.NotEmpty(t, report.SlowestTests)
	})

	t.Run("When fixture has 3 runs the trend should have 3 entries in chronological order", func(t *testing.T) {
		require.Len(t, report.TrendEntries, 3)
		require.Equal(t, "2026-01-04", report.TrendEntries[0].Date)
		require.Equal(t, "2026-01-06", report.TrendEntries[2].Date)
	})

	t.Run("When clean and flaky counts are summed they should equal total specs tracked", func(t *testing.T) {
		total := report.CleanCount + report.FlakyCount + report.ConsistentlyFailingCount
		require.Equal(t, report.TotalAnalyzedSpecs, total)
	})

	t.Run("When a spec is flaky it should appear in FlakeEntries", func(t *testing.T) {
		require.NotEmpty(t, report.FlakeEntries)
	})
}

// --------------------------------------------------------------------------
// Fixture loading sanity check
// --------------------------------------------------------------------------

// TestLoadFixture verifies the testdata file parses correctly.
func TestLoadFixture(t *testing.T) {
	t.Run("raw-junit.json should parse as rawFile", func(t *testing.T) {
		var raw rawFile
		require.NoError(t, loadJSON("testdata/raw-junit.json", &raw))
		require.NotEmpty(t, raw.Meta.Workflow)
		require.NotEmpty(t, raw.Runs)
	})

	t.Run("raw-junit.json fixture helper should return content string", func(t *testing.T) {
		content := loadFixture(t, "raw-junit.json")
		require.Contains(t, content, "runs")
	})
}

// TestParseJUnitFile verifies XML parsing for both Ginkgo and Go test formats.
func TestParseJUnitFile(t *testing.T) {
	t.Run("When JUnit XML has Ginkgo format it should extract the spec name correctly", func(t *testing.T) {
		xml := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Store Suite">
    <testcase name="[It] Store Suite should create a device [12345, integration]" classname="Store Suite" time="1.5"/>
    <testcase name="[It] Store Suite should fail [67890, integration]" classname="Store Suite" time="2.0">
      <failure message="database error">detail here</failure>
    </testcase>
    <testcase name="[BeforeSuite]" classname="Store Suite" time="0.3"/>
  </testsuite>
</testsuites>`
		path := filepath.Join(t.TempDir(), "junit.xml")
		require.NoError(t, os.WriteFile(path, []byte(xml), 0o644))

		results, err := parseJUnitFile(path)
		require.NoError(t, err)
		require.Len(t, results, 2)

		var passing, failing *junitResult
		for i := range results {
			if results[i].Passed {
				passing = &results[i]
			} else {
				failing = &results[i]
			}
		}
		require.NotNil(t, passing)
		require.Equal(t, "Store Suite should create a device", passing.SpecName)
		require.InDelta(t, 1.5, passing.DurationSec, 0.001)

		require.NotNil(t, failing)
		require.Equal(t, "Store Suite should fail", failing.SpecName)
		require.Equal(t, "database error", failing.FailureMsg)
	})

	t.Run("When JUnit XML has standard Go test format it should build classname/TestName keys", func(t *testing.T) {
		xml := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="github.com/flightctl/flightctl/internal/store">
    <testcase name="TestCreateDevice" classname="github.com/flightctl/flightctl/internal/store" time="0.05"/>
    <testcase name="TestCreateDevice/valid_input" classname="github.com/flightctl/flightctl/internal/store" time="0.02"/>
  </testsuite>
</testsuites>`
		path := filepath.Join(t.TempDir(), "junit.xml")
		require.NoError(t, os.WriteFile(path, []byte(xml), 0o644))

		results, err := parseJUnitFile(path)
		require.NoError(t, err)
		require.Len(t, results, 2)

		names := make(map[string]bool)
		for _, r := range results {
			names[r.SpecName] = true
		}
		require.True(t, names["internal/store/TestCreateDevice"])
		require.True(t, names["internal/store/TestCreateDevice/valid_input"])
	})

	t.Run("When JUnit XML has a skipped test case it should be marked as skipped", func(t *testing.T) {
		xml := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Suite">
    <testcase name="TestSkipped" classname="pkg/foo" time="0">
      <skipped/>
    </testcase>
  </testsuite>
</testsuites>`
		path := filepath.Join(t.TempDir(), "junit.xml")
		require.NoError(t, os.WriteFile(path, []byte(xml), 0o644))

		results, err := parseJUnitFile(path)
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.True(t, results[0].Skipped)
		require.False(t, results[0].Passed)
	})
}

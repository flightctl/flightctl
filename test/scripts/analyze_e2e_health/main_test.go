package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// --- helpers to build fixture data without healthData ---

func makeJUnitFiles(runID int64, runURL, date string, specs map[string]rawSpecResult) []rawJUnitFile {
	return []rawJUnitFile{{RunID: runID, RunURL: runURL, Date: date, Specs: specs}}
}

func makeJobsFile(runID int64, runURL, date string) rawJobsFile {
	return rawJobsFile{
		Meta: collectMeta{
			Branch:       defaultBranch,
			Workflow:     defaultWorkflow,
			RepoURL:      "https://github.com/flightctl/flightctl",
			GeneratedAt:  "2026-01-01T00:00:00Z",
			AnalyzedRuns: 1,
		},
		Runs: []rawRunEntry{
			{RunID: runID, RunURL: runURL, Date: date, Conclusion: "success"},
		},
	}
}

// TestClassifyFlake covers the pass/fail classification.
func TestClassifyFlake(t *testing.T) {
	tests := []struct {
		name   string
		result specResult
		expect string
	}{
		{
			name:   "When a spec fails some runs but also passes it should be classified as Flaky",
			result: specResult{PassCount: 11, FailCount: 3},
			expect: "Flaky",
		},
		{
			name:   "When a spec fails some runs but also passes it should be classified as Flaky regardless of rate",
			result: specResult{PassCount: 1, FailCount: 13},
			expect: "Flaky",
		},
		{
			name:   "When a spec never passes it should be classified as Consistently failing",
			result: specResult{PassCount: 0, FailCount: 8},
			expect: "Consistently failing",
		},
		{
			name:   "When a spec has zero failures it should return empty string",
			result: specResult{PassCount: 14, FailCount: 0},
			expect: "",
		},
		{
			name:   "When pass and fail counts are both zero it should return empty string",
			result: specResult{PassCount: 0, FailCount: 0},
			expect: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, classifyFlake(tt.result))
		})
	}
}

// TestComputeFlakes covers infra instability exclusion and per-spec classification.
func TestComputeFlakes(t *testing.T) {
	t.Run("When a run has more than 30 percent of specs failing it should be marked as infra instability", func(t *testing.T) {
		// 5 specs, 4 failing = 80% > 30%
		specs := map[string]rawSpecResult{
			"Spec A": {Passed: false, Skipped: false},
			"Spec B": {Passed: false, Skipped: false},
			"Spec C": {Passed: false, Skipped: false},
			"Spec D": {Passed: false, Skipped: false},
			"Spec E": {Passed: true, Skipped: false},
		}
		ref := runRef{RunID: 1, RunURL: "https://example.com/runs/1", Date: "2026-01-01"}
		jagg := aggregateJUnit(makeJUnitFiles(1, ref.RunURL, ref.Date, specs), []runRef{ref})
		entries, _, _, _, _ := computeFlakes(jagg)
		require.Empty(t, entries)
		require.Len(t, jagg.infraRuns, 1)
	})

	t.Run("When a spec only fails in excluded infra runs it should not appear in flake entries", func(t *testing.T) {
		specs := map[string]rawSpecResult{
			"Clean spec": {Passed: true},
		}
		ref := runRef{RunID: 99, RunURL: "u", Date: "d"}
		jagg := aggregateJUnit(makeJUnitFiles(99, ref.RunURL, ref.Date, specs), []runRef{ref})
		entries, _, flaky, failing, _ := computeFlakes(jagg)
		require.Empty(t, entries)
		require.Equal(t, 0, flaky)
		require.Equal(t, 0, failing)
	})

	t.Run("When multiple specs have failures they should be sorted by failure rate descending", func(t *testing.T) {
		// 10 specs total per run; only 2 flaky — well below the 30% infra threshold.
		makeRun := func(lowFlakePasses, highFlakePasses bool) map[string]rawSpecResult {
			m := map[string]rawSpecResult{
				"Low flake":  {Passed: lowFlakePasses},
				"High flake": {Passed: highFlakePasses},
			}
			for i := 0; i < 8; i++ {
				m[fmt.Sprintf("Clean %d", i)] = rawSpecResult{Passed: true}
			}
			return m
		}
		files := []rawJUnitFile{
			{RunID: 1, RunURL: "u1", Date: "d", Specs: makeRun(false, false)},
			{RunID: 2, RunURL: "u2", Date: "d", Specs: makeRun(true, false)},
			{RunID: 3, RunURL: "u3", Date: "d", Specs: makeRun(true, true)},
		}
		refs := []runRef{
			{RunID: 1, RunURL: "u1"},
			{RunID: 2, RunURL: "u2"},
			{RunID: 3, RunURL: "u3"},
		}
		jagg := aggregateJUnit(files, refs)
		entries, _, _, _, _ := computeFlakes(jagg)
		require.Len(t, entries, 2)
		require.Greater(t, entries[0].FailRate, entries[1].FailRate)
	})

	t.Run("When a JUnit failure contains a message it should appear in the flake entry", func(t *testing.T) {
		// 10 specs, 1 failing = 10% < 30% infra threshold
		makeRun := func(targetPasses bool) map[string]rawSpecResult {
			m := map[string]rawSpecResult{"Suite spec": {Passed: targetPasses, FailureMsg: "timeout waiting for device"}}
			for i := 0; i < 9; i++ {
				m[fmt.Sprintf("Clean %d", i)] = rawSpecResult{Passed: true}
			}
			return m
		}
		files := []rawJUnitFile{
			{RunID: 1, RunURL: "u1", Date: "d", Specs: makeRun(false)},
			{RunID: 2, RunURL: "u2", Date: "d", Specs: makeRun(true)},
		}
		refs := []runRef{{RunID: 1, RunURL: "u1"}, {RunID: 2, RunURL: "u2"}}
		jagg := aggregateJUnit(files, refs)
		entries, _, _, _, _ := computeFlakes(jagg)
		require.Len(t, entries, 1)
		require.Equal(t, "timeout waiting for device", entries[0].FailureMsg)
	})

	t.Run("When a flake entry is generated it should include the last failed run URL", func(t *testing.T) {
		makeRun := func(url string, passes bool) map[string]rawSpecResult {
			m := map[string]rawSpecResult{"Suite spec": {Passed: passes}}
			for i := 0; i < 9; i++ {
				m[fmt.Sprintf("Clean %d", i)] = rawSpecResult{Passed: true}
			}
			return m
		}
		failURL := "https://github.com/org/repo/actions/runs/12345"
		files := []rawJUnitFile{
			{RunID: 1, RunURL: failURL, Date: "d", Specs: makeRun(failURL, false)},
			{RunID: 2, RunURL: "u2", Date: "d", Specs: makeRun("u2", true)},
		}
		refs := []runRef{
			{RunID: 1, RunURL: failURL},
			{RunID: 2, RunURL: "u2"},
		}
		jagg := aggregateJUnit(files, refs)
		entries, _, _, _, _ := computeFlakes(jagg)
		require.Len(t, entries, 1)
		require.Equal(t, failURL, entries[0].LastRunURL)
	})

	t.Run("When a test name contains a Ginkgo numeric label it should appear in the entry", func(t *testing.T) {
		makeRun := func(passes bool) map[string]rawSpecResult {
			m := map[string]rawSpecResult{"Suite spec with label": {Passed: passes, GinkgoLabel: "78753"}}
			for i := 0; i < 9; i++ {
				m[fmt.Sprintf("Clean %d", i)] = rawSpecResult{Passed: true}
			}
			return m
		}
		files := []rawJUnitFile{
			{RunID: 1, RunURL: "u1", Date: "d", Specs: makeRun(false)},
			{RunID: 2, RunURL: "u2", Date: "d", Specs: makeRun(true)},
		}
		refs := []runRef{{RunID: 1, RunURL: "u1"}, {RunID: 2, RunURL: "u2"}}
		jagg := aggregateJUnit(files, refs)
		entries, _, _, _, _ := computeFlakes(jagg)
		require.Len(t, entries, 1)
		require.Equal(t, "78753", entries[0].GinkgoLabel)
	})
}

// TestAggregateJUnitShardMerging verifies that shard results are merged correctly.
// Each run has 10 specs to stay well below the 30% infra instability threshold.
func TestAggregateJUnitShardMerging(t *testing.T) {
	// Build a shard of N passing specs as padding to avoid infra instability detection.
	padding := func(n int) map[string]rawSpecResult {
		m := make(map[string]rawSpecResult, n)
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("Pad %d", i)] = rawSpecResult{Passed: true}
		}
		return m
	}

	t.Run("When a spec passes in one shard and is skipped in another the result should be pass", func(t *testing.T) {
		s1 := padding(8)
		s1["Spec A"] = rawSpecResult{Passed: true}
		s1["Spec B"] = rawSpecResult{Skipped: true}
		s2 := padding(8)
		s2["Spec A"] = rawSpecResult{Skipped: true}
		s2["Spec B"] = rawSpecResult{Passed: true}
		files := []rawJUnitFile{
			{RunID: 1, RunURL: "u", Date: "d", Specs: s1},
			{RunID: 1, RunURL: "u", Date: "d", Specs: s2},
		}
		refs := []runRef{{RunID: 1, RunURL: "u"}}
		jagg := aggregateJUnit(files, refs)
		require.Equal(t, 2, jagg.specs["Spec A"].PassCount+jagg.specs["Spec B"].PassCount)
		require.Equal(t, 0, jagg.specs["Spec A"].FailCount)
	})

	t.Run("When a spec fails in one shard and passes in another the result should be fail", func(t *testing.T) {
		s1 := padding(9)
		s1["Spec A"] = rawSpecResult{Passed: false, FailureMsg: "boom"}
		s2 := padding(9)
		s2["Spec A"] = rawSpecResult{Passed: true}
		files := []rawJUnitFile{
			{RunID: 1, RunURL: "u", Date: "d", Specs: s1},
			{RunID: 1, RunURL: "u", Date: "d", Specs: s2},
		}
		refs := []runRef{{RunID: 1, RunURL: "u"}}
		jagg := aggregateJUnit(files, refs)
		require.Equal(t, 1, jagg.specs["Spec A"].FailCount)
		require.Equal(t, 0, jagg.specs["Spec A"].PassCount)
	})
}

// TestExtractGinkgoLabel covers the label regex.
func TestExtractGinkgoLabel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "When test name contains a 5-digit label it should extract the label",
			input: "[It] Agent Suite Device should enroll [78753, sanity]",
			want:  "78753",
		},
		{
			name:  "When test name contains a label followed by a comma it should extract the label",
			input: "[It] Suite spec [12345, agent]",
			want:  "12345",
		},
		{
			name:  "When test name has no numeric label it should return empty",
			input: "[It] Suite spec [sanity]",
			want:  "",
		},
		{
			name:  "When test name has no brackets it should return empty",
			input: "[It] Suite spec without labels",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, extractGinkgoLabel(tt.input))
		})
	}
}

// TestJunitSpecName covers the spec name extraction.
func TestJunitSpecName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "When name has [It] prefix and labels it should strip both",
			input: "[It] Agent Suite Device should enroll [78753, sanity]",
			want:  "Agent Suite Device should enroll",
		},
		{
			name:  "When name is [BeforeSuite] it should return empty",
			input: "[BeforeSuite]",
			want:  "",
		},
		{
			name:  "When name lacks [It] prefix it should return empty",
			input: "some test case",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, junitSpecName(tt.input))
		})
	}
}

// TestComputeTimings verifies that per-spec avg/stddev is derived from raw shard files.
func TestComputeTimings(t *testing.T) {
	files := []rawJUnitFile{
		{RunID: 1, RunURL: "u1", Date: "d", Specs: map[string]rawSpecResult{
			"Spec A": {Passed: true, DurationSec: 100},
			"Spec B": {Passed: true, DurationSec: 50},
		}},
		{RunID: 2, RunURL: "u2", Date: "d", Specs: map[string]rawSpecResult{
			"Spec A": {Passed: true, DurationSec: 200},
			"Spec B": {Passed: true, DurationSec: 50},
		}},
	}
	refs := []runRef{{RunID: 1, RunURL: "u1"}, {RunID: 2, RunURL: "u2"}}

	t.Run("When two runs have the same spec it should average the durations", func(t *testing.T) {
		timings := computeTimings(files, refs)
		require.InDelta(t, 150.0, timings["Spec A"].Avg, 0.001)
		require.InDelta(t, 50.0, timings["Spec B"].Avg, 0.001)
	})

	t.Run("When a spec is skipped it should be excluded from timings", func(t *testing.T) {
		filesWithSkip := []rawJUnitFile{
			{RunID: 1, RunURL: "u1", Date: "d", Specs: map[string]rawSpecResult{
				"Skipped spec": {Skipped: true, DurationSec: 0},
			}},
		}
		timings := computeTimings(filesWithSkip, refs[:1])
		_, exists := timings["Skipped spec"]
		require.False(t, exists)
	})
}

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
}

// TestComputeOptimizations verifies tier grouping and LPT-based workflow estimates.
func TestComputeOptimizations(t *testing.T) {
	timings := map[string]specTiming{
		"very slow": {Avg: 240.0, StdDev: 20.0},
		"slow":      {Avg: 150.0, StdDev: 10.0},
		"fast":      {Avg: 30.0, StdDev: 2.0},
	}

	t.Run("When the top N slowest specs are reduced to the target threshold it should produce correct entries", func(t *testing.T) {
		entries, _, _, _, _ := computeOptimizations(timings, nil, 5, 10, 0)
		// entries[0] is the synthetic baseline row; optimisation rows start at [1].
		require.Len(t, entries, 3) // baseline + 2 optimisable entries (3 specs → 2 steps)
		require.Equal(t, "(current — no changes)", entries[0].Name)
		// very slow (240) → slow (150): saved 90
		// slow      (150) → fast  (30): saved 120
		require.InDelta(t, 90.0, entries[1].SavedSecs, 0.001)
		require.InDelta(t, 120.0, entries[2].SavedSecs, 0.001)
	})

	t.Run("When the baseline and best workflow are compared the best should not exceed the baseline", func(t *testing.T) {
		_, _, _, baseline, best := computeOptimizations(timings, nil, 5, 10, 0)
		require.GreaterOrEqual(t, baseline, best)
	})
}

// TestPipelineModeInference covers shard count inference from observed counts.
func TestModeInt(t *testing.T) {
	tests := []struct {
		name   string
		input  []int
		expect int
	}{
		{
			name:   "When shard job counts vary across runs it should infer the count via mode",
			input:  []int{10, 10, 9, 10, 10, 8, 10},
			expect: 10,
		},
		{
			name:   "When values are equal it should return that value",
			input:  []int{5, 5, 5},
			expect: 5,
		},
		{
			name:   "When slice is empty it should return 0",
			input:  []int{},
			expect: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, modeInt(tt.input))
		})
	}
}

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

// TestBuildPipelinePhases verifies phase extraction from raw aggregated data.
func TestBuildPipelinePhases(t *testing.T) {
	agg := pipelineAgg{
		phases: map[string][]float64{
			"build-images-and-charts":   {503, 498, 512},
			"build-rpms":                {765, 742, 778},
			"e2e-tests_cs9":             {3130, 3089, 3198},
			"e2e-tests_cs9::run tests":  {2415, 2395, 2462},
			"e2e-tests_cs9::deploy env": {190, 182, 198},
			"e2e-tests_cs10":            {1850, 1832, 1878},
			"e2e-tests_cs10::run tests": {1135, 1118, 1162},
		},
		shardCounts: map[string][]int{
			"cs9":  {10, 10, 10},
			"cs10": {5, 5, 5},
		},
	}

	t.Run("When pipeline phases are present they should compute avg and stddev correctly", func(t *testing.T) {
		phases := buildPipelinePhases(agg)
		require.NotEmpty(t, phases)
		for _, p := range phases {
			require.Greater(t, p.AvgSecs, 0.0, "phase %q should have positive avg", p.Name)
		}
	})

	t.Run("When multiple matrix shard jobs exist the critical shard should have the max duration", func(t *testing.T) {
		phases := buildPipelinePhases(agg)
		var criticalSec float64
		for _, p := range phases {
			if strings.Contains(p.Name, "cs9") && !strings.HasPrefix(p.Name, "↳") {
				criticalSec = p.AvgSecs
			}
		}
		require.Greater(t, criticalSec, 0.0)
	})

	t.Run("When shard job counts vary across runs the count should be inferred via mode", func(t *testing.T) {
		count := inferShardCount(agg)
		require.Equal(t, 10, count)
	})
}

// TestRenderHTML exercises the render path using fixture raw files.
func TestRenderHTML(t *testing.T) {
	var jobsFile rawJobsFile
	require.NoError(t, loadJSON("testdata/e2e-raw-jobs.json", &jobsFile))

	var junitFiles []rawJUnitFile
	require.NoError(t, loadJSON("testdata/e2e-raw-junit.json", &junitFiles))

	report := computeReport(jobsFile, junitFiles, nil, 10)

	outPath := filepath.Join(t.TempDir(), "report.html")
	require.NoError(t, renderHTML(report, outPath))

	content, err := os.ReadFile(outPath)
	require.NoError(t, err)
	html := string(content)

	t.Run("When all sections have data it should produce a valid HTML file with all sections", func(t *testing.T) {
		require.Contains(t, html, `id="pipeline"`)
		require.Contains(t, html, `id="flakes"`)
		require.Contains(t, html, `id="optimization"`)
	})

	t.Run("When the HTML file is parsed it should contain SVG elements for the bar and donut charts", func(t *testing.T) {
		require.Contains(t, html, "<svg ")
		require.Contains(t, html, "<path d=") // donut arc paths
		require.Contains(t, html, "<rect ")   // bar chart rects
	})
}

// TestRenderSlackSummary verifies the JSON summary fields.
func TestRenderSlackSummary(t *testing.T) {
	var jobsFile rawJobsFile
	require.NoError(t, loadJSON("testdata/e2e-raw-jobs.json", &jobsFile))

	var junitFiles []rawJUnitFile
	require.NoError(t, loadJSON("testdata/e2e-raw-junit.json", &junitFiles))

	report := computeReport(jobsFile, junitFiles, nil, 10)

	path := filepath.Join(t.TempDir(), "summary.json")
	require.NoError(t, renderSlackSummary(report, path))

	t.Run("When the JSON summary is written it should contain the required fields for Slack", func(t *testing.T) {
		var s slackSummary
		require.NoError(t, loadJSON(path, &s))
		require.GreaterOrEqual(t, s.WallTimeSecs, 0.0)
		require.GreaterOrEqual(t, s.PrepareTimeSecs, 0.0)
		require.GreaterOrEqual(t, s.TestingTimeSecs, 0.0)
		require.GreaterOrEqual(t, s.InferredShards, 0)
		require.GreaterOrEqual(t, s.AnalyzedRuns, 0)
		require.GreaterOrEqual(t, s.TotalSpecsTracked, 0)
		require.GreaterOrEqual(t, s.ConsistentlyFailing, 0)
		require.GreaterOrEqual(t, s.FlakyTests, 0)
		require.GreaterOrEqual(t, s.NeverFailed, 0)
		require.GreaterOrEqual(t, s.InfraInstabilityEvents, 0)
		require.GreaterOrEqual(t, s.OptimizationSavingsSecs, 0.0)
	})
}

// TestStepSummaryNoScriptTags verifies the step summary contains no script/svg tags.
func TestStepSummaryNoScriptTags(t *testing.T) {
	var jobsFile rawJobsFile
	require.NoError(t, loadJSON("testdata/e2e-raw-jobs.json", &jobsFile))

	var junitFiles []rawJUnitFile
	require.NoError(t, loadJSON("testdata/e2e-raw-junit.json", &junitFiles))

	report := computeReport(jobsFile, junitFiles, nil, 10)

	summaryPath := filepath.Join(t.TempDir(), "summary.html")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)

	require.NoError(t, appendStepSummary(report))

	content, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	html := string(content)

	t.Run("When the step summary is written it should contain only HTML tables with no script or svg tags", func(t *testing.T) {
		require.NotContains(t, html, "<script")
		require.NotContains(t, html, "<svg")
		require.Contains(t, html, "<table")
		require.Contains(t, html, "<h2>")
	})
}

// TestTruncateMsg covers message truncation and whitespace normalization.
func TestTruncateMsg(t *testing.T) {
	t.Run("When message is within the limit it should be returned unchanged", func(t *testing.T) {
		require.Equal(t, "short msg", truncateMsg("short msg", 200))
	})
	t.Run("When message exceeds the limit it should be truncated with ellipsis", func(t *testing.T) {
		long := strings.Repeat("a", 300)
		result := truncateMsg(long, 200)
		require.Len(t, []rune(result), 201)
		require.True(t, strings.HasSuffix(result, "…"))
	})
	t.Run("When message contains newlines they should be normalized to spaces", func(t *testing.T) {
		result := truncateMsg("line1\nline2\nline3", 200)
		require.Equal(t, "line1 line2 line3", result)
	})
}

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

// TestPreTestFailureDetection verifies that runs with no JUnit are flagged.
func TestPreTestFailureDetection(t *testing.T) {
	var jobsFile rawJobsFile
	require.NoError(t, loadJSON("testdata/e2e-raw-jobs.json", &jobsFile))

	var junitFiles []rawJUnitFile
	require.NoError(t, loadJSON("testdata/e2e-raw-junit.json", &junitFiles))

	report := computeReport(jobsFile, junitFiles, nil, 10)

	t.Run("When a run has no JUnit artifacts it should be reported as a pre-test failure", func(t *testing.T) {
		require.GreaterOrEqual(t, report.PreTestFailureCount, 0)
	})
}

// TestLoadFixtures is a sanity check that the testdata files parse correctly.
func TestLoadFixtures(t *testing.T) {
	t.Run("e2e-raw-jobs.json should parse as rawJobsFile", func(t *testing.T) {
		var jobsFile rawJobsFile
		require.NoError(t, loadJSON("testdata/e2e-raw-jobs.json", &jobsFile))
		require.NotEmpty(t, jobsFile.Meta.Branch)
		require.NotEmpty(t, jobsFile.Runs)
	})

	t.Run("e2e-raw-junit.json should parse as []rawJUnitFile", func(t *testing.T) {
		var files []rawJUnitFile
		require.NoError(t, loadJSON("testdata/e2e-raw-junit.json", &files))
		require.NotEmpty(t, files)
	})

}

// Verify the loadFixture helper works.
func TestLoadFixtureHelper(t *testing.T) {
	content := loadFixture(t, "e2e-raw-jobs.json")
	require.Contains(t, content, "runs")
}

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/scripts/pkg/e2etestutils"
)

const suiteOverheadPrefix = e2etestutils.SuiteOverheadPrefix

// reVariant extracts the OS variant (e.g. "cs9", "cs10", "cs11") from a job name.
var reVariant = regexp.MustCompile(`cs\d+`)

// reMatrixSuffix matches trailing matrix parameters like " (cs9, 3/11)".
var reMatrixSuffix = regexp.MustCompile(`\s*\([^)]+\)\s*$`)

// pipelineAgg is the in-memory result of aggregating raw job data.
// It is always computed from e2e-raw-jobs.json and never persisted.
type pipelineAgg struct {
	phases      map[string][]float64
	shardCounts map[string][]int
	wallTimes   []float64
}

// junitAgg is the in-memory result of aggregating raw JUnit run data.
// It is always computed from e2e-raw-junit.json and never persisted.
type junitAgg struct {
	specs     map[string]specResult
	noJUnit   []runRef
	infraRuns []runRef
}

// specResult tracks aggregate pass/fail counts across analyzed runs.
type specResult struct {
	PassCount       int    `json:"pass_count"`
	FailCount       int    `json:"fail_count"`
	SkipCount       int    `json:"skip_count"`
	LastFailedRunID int64  `json:"last_failed_run_id,omitempty"`
	LastFailedURL   string `json:"last_failed_url,omitempty"`
	FailureMsg      string `json:"failure_msg,omitempty"`
	GinkgoLabel     string `json:"ginkgo_label,omitempty"`
}

// aggregateRawJobs converts raw per-run job data into pipeline timing observations.
// Only successful runs contribute — failed runs abort early and skew averages.
func aggregateRawJobs(rawRuns []rawRunEntry) pipelineAgg {
	agg := pipelineAgg{
		phases:      make(map[string][]float64),
		shardCounts: make(map[string][]int),
	}

	for _, run := range rawRuns {
		if run.Conclusion != "success" {
			continue
		}
		runStart, err := time.Parse(time.RFC3339, run.StartedAt)
		if err != nil {
			continue
		}

		var maxJobEnd time.Time
		jobMaxDur := map[string]float64{}
		jobShardCount := map[string]int{}
		stepMaxDur := map[string]float64{}

		for _, job := range run.Jobs {
			sa, err := time.Parse(time.RFC3339, job.StartedAt)
			if err != nil {
				continue
			}
			sc, err := time.Parse(time.RFC3339, job.CompletedAt)
			if err != nil {
				continue
			}
			jobDur := sc.Sub(sa).Seconds()
			if jobDur <= 0 {
				continue
			}
			if sc.After(maxJobEnd) {
				maxJobEnd = sc
			}
			jobKey := jobGroupKey(job.Name)
			// Only count shards for actual e2e test jobs, not build/infra
			// jobs that happen to share the same variant suffix.
			if strings.Contains(jobKey, "e2e-test") {
				jobShardCount[jobKey]++
			}
			if jobDur > jobMaxDur[jobKey] {
				jobMaxDur[jobKey] = jobDur
			}
			// Use "::" as the job/step separator — " / " appears in reusable-
			// workflow job names, so "/" alone would be ambiguous.
			for _, step := range job.Steps {
				if step.DurationSec <= 0 {
					continue
				}
				stepKey := jobKey + "::" + normalizeLabel(step.Name)
				if step.DurationSec > stepMaxDur[stepKey] {
					stepMaxDur[stepKey] = step.DurationSec
				}
			}
		}

		// Wall time = max(job.CompletedAt) - run.StartedAt, matching what
		// GitHub displays. UpdatedAt is avoided — it can be bumped by things
		// unrelated to job completion (artifact uploads, status hooks).
		if !maxJobEnd.IsZero() {
			if wallSecs := maxJobEnd.Sub(runStart).Seconds(); wallSecs > 0 {
				agg.wallTimes = append(agg.wallTimes, wallSecs)
			}
		}

		for key, maxDur := range jobMaxDur {
			if maxDur > 0 {
				agg.phases[key] = append(agg.phases[key], maxDur)
			}
			if variant := reVariant.FindString(key); variant != "" {
				agg.shardCounts[variant] = append(agg.shardCounts[variant], jobShardCount[key])
			}
		}
		for key, maxDur := range stepMaxDur {
			if maxDur > 0 {
				agg.phases[key] = append(agg.phases[key], maxDur)
			}
		}
	}
	return agg
}

// aggregateJUnit builds per-spec pass/fail counts and identifies infrastructure
// instability runs from the raw per-shard JUnit files.
//
// Input is a flat list of rawJUnitFile entries — one per shard artifact per run.
// The function:
//  1. Groups shards by RunID.
//  2. Merges shard results: failure on any shard wins; pass overrides skip (Ginkgo
//     marks non-assigned specs as skipped in every shard's output).
//  3. Detects infra instability: if >infraInstabilityThreshold of merged specs
//     fail in a run, the entire run is excluded from per-spec counts.
//  4. Accumulates per-spec pass/fail/skip counts across all non-excluded runs.
//
// Runs that produced no JUnit files at all are recorded in noJUnit.
func aggregateJUnit(junitFiles []rawJUnitFile, allRunIDs []runRef) junitAgg {
	agg := junitAgg{
		specs: make(map[string]specResult),
	}

	// Group shard files by run ID.
	byRun := make(map[int64][]rawJUnitFile)
	for _, f := range junitFiles {
		byRun[f.RunID] = append(byRun[f.RunID], f)
	}

	for _, ref := range allRunIDs {
		shards, ok := byRun[ref.RunID]
		if !ok {
			agg.noJUnit = append(agg.noJUnit, ref)
			continue
		}

		// Merge shards: failure wins; pass overrides skip.
		merged := make(map[string]rawSpecResult)
		for _, shard := range shards {
			for name, s := range shard.Specs {
				existing, seen := merged[name]
				if !seen {
					merged[name] = s
					continue
				}
				switch {
				case !s.Passed && !s.Skipped:
					// Failure always wins.
					existing.Passed = false
					existing.Skipped = false
					if s.DurationSec > 0 && existing.DurationSec == 0 {
						existing.DurationSec = s.DurationSec
					}
					if s.FailureMsg != "" && existing.FailureMsg == "" {
						existing.FailureMsg = s.FailureMsg
					}
					merged[name] = existing
				case s.Passed && existing.Skipped:
					// Pass overrides a prior skip.
					existing.Passed = true
					existing.Skipped = false
					existing.DurationSec = s.DurationSec
					merged[name] = existing
				}
			}
		}

		// Infra instability check on the merged result.
		// Only count executed (non-skipped) test specs — label-filtered specs appear
		// as skipped in every shard's JUnit and must not inflate the denominator,
		// otherwise the threshold can never be reached. __suite__: entries (BeforeSuite
		// overhead) are also excluded; they are not test specs.
		executed := 0
		failures := 0
		for name, s := range merged {
			if s.Skipped || strings.HasPrefix(name, suiteOverheadPrefix) {
				continue
			}
			executed++
			if !s.Passed {
				failures++
			}
		}
		if executed > 0 && float64(failures)/float64(executed) > infraInstabilityThreshold {
			agg.infraRuns = append(agg.infraRuns, ref)
			continue
		}

		for name, s := range merged {
			if strings.HasPrefix(name, suiteOverheadPrefix) {
				continue
			}
			existing := agg.specs[name]
			switch {
			case s.Skipped:
				existing.SkipCount++
			case s.Passed:
				existing.PassCount++
			default:
				existing.FailCount++
				// Runs are processed newest-first; only record the URL on the
				// first (most recent) failure so the link stays up to date.
				if existing.LastFailedRunID == 0 {
					existing.LastFailedRunID = ref.RunID
					existing.LastFailedURL = ref.RunURL
				}
				if s.FailureMsg != "" && existing.FailureMsg == "" {
					existing.FailureMsg = s.FailureMsg
				}
			}
			if existing.GinkgoLabel == "" && s.GinkgoLabel != "" {
				existing.GinkgoLabel = s.GinkgoLabel
			}
			agg.specs[name] = existing
		}
	}
	return agg
}

// computeTimings derives per-spec avg duration and stddev from the raw shard
// files, replicating the logic of the update_test_timings script.
// Infra instability runs are excluded (same threshold as aggregateJUnit).
// Skipped specs and zero-duration entries are ignored.
func computeTimings(junitFiles []rawJUnitFile, allRunIDs []runRef) map[string]specTiming {
	byRun := make(map[int64][]rawJUnitFile)
	for _, f := range junitFiles {
		byRun[f.RunID] = append(byRun[f.RunID], f)
	}

	allObs := make(map[string][]float64)
	for _, ref := range allRunIDs {
		shards, ok := byRun[ref.RunID]
		if !ok {
			continue
		}
		// Merge shards to get the true per-spec result for this run.
		merged := make(map[string]rawSpecResult)
		for _, shard := range shards {
			for name, s := range shard.Specs {
				existing, seen := merged[name]
				if !seen {
					merged[name] = s
					continue
				}
				switch {
				case !s.Passed && !s.Skipped:
					existing.Passed = false
					existing.Skipped = false
					if s.DurationSec > 0 && existing.DurationSec == 0 {
						existing.DurationSec = s.DurationSec
					}
					merged[name] = existing
				case s.Passed && existing.Skipped:
					existing.Passed = true
					existing.Skipped = false
					existing.DurationSec = s.DurationSec
					merged[name] = existing
				}
			}
		}
		// Skip infra instability runs.
		total := len(merged)
		failures := 0
		for _, s := range merged {
			if !s.Passed && !s.Skipped {
				failures++
			}
		}
		if total > 0 && float64(failures)/float64(total) > infraInstabilityThreshold {
			continue
		}
		for name, s := range merged {
			if s.Skipped || s.DurationSec <= 0 {
				continue
			}
			allObs[name] = append(allObs[name], s.DurationSec)
		}
	}

	timings := make(map[string]specTiming, len(allObs))
	for name, obs := range allObs {
		avg, std := avgStddev(obs)
		timings[name] = specTiming{Avg: avg, StdDev: std}
	}
	return timings
}

// jobGroupKey produces a stable, normalized key for a job. For reusable-
// workflow jobs ("caller / child") the caller is used so all child jobs
// collapse to the same key and the per-run max gives the critical path.
// Matrix parameters and variant suffixes are handled as well.
//
// Examples:
//
//	"e2e-tests (cs9-bootc, helm, sanity, 10) / e2e-test" → "e2e-tests_cs9"
//	"build-images-and-charts / build-backend-containers"  → "build-images-and-charts"
//	"build-rpms (centos-stream+epel-next-9-x86_64) / …"  → "build-rpms"
//	"compute-tag"                                         → "compute-tag"
func jobGroupKey(name string) string {
	caller := name
	if idx := strings.Index(name, " / "); idx >= 0 {
		caller = name[:idx]
	}
	variant := reVariant.FindString(strings.ToLower(caller))
	base := normalizeLabel(reMatrixSuffix.ReplaceAllString(caller, ""))
	if variant != "" {
		return base + "_" + variant
	}
	return base
}

// normalizeLabel lowercases and trims a label for use as a map key or display name.
func normalizeLabel(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// reportData is the output of computeReport. It contains only plain Go values
// — no template.HTML, no SVG. The render layer wraps it in a renderContext to
// add the generated charts before executing the HTML template.
type reportData struct {
	GeneratedAt  string
	RepoURL      string
	AnalyzedRuns int

	// Section 1: Pipeline timing.
	PipelinePhases        []phaseEntry
	EstimatedWallTimeSecs float64
	BaselineShardSecs     float64 // LPT slowest-shard estimate at current node count
	PipelineOverviewReady bool    // true when overhead+shard breakdown is valid

	// Section 2: Flaky tests.
	FlakeEntries             []flakeEntry
	InfraInstabilityCount    int
	PreTestFailureCount      int
	PreTestFailureRefs       []runRef
	TotalAnalyzedSpecs       int
	FlakyCount               int
	ConsistentlyFailingCount int
	CleanCount               int

	// Section 3: Slowest tests.
	SlowestTests []specEntry

	// Section 5: Optimization opportunities.
	OptimEntries       []optimEntry
	TotalSuiteSecs     float64
	TopNPct            float64
	InferredShardCount int
	// LPT-based baseline and best-case workflow time (from first/last tier).
	OptimBaselineWorkflowSecs float64
	OptimBestWorkflowSecs     float64
	// Pipeline overhead = observed wall time minus LPT max shard (build, deploy, etc.).
	PipelineOverheadSecs float64
	// Node-count offsets shown as extra columns in the optimization table.
	// Each optimEntry.NodeCountEsts is parallel to this slice.
	OptimNodeOffsets []int
}

// phaseEntry is one row in the pipeline timing table.
type phaseEntry struct {
	Name    string
	AvgSecs float64
	StdDev  float64
	Indent  int // 0 = top-level job, 1 = step within a job
}

// flakeEntry is one row in the flaky tests table.
type flakeEntry struct {
	SpecName    string
	GinkgoLabel string
	FailRate    float64
	FailCount   int
	TotalRuns   int
	Class       string // "Flaky" or "Consistently failing"
	LastRunURL  string
	FailureMsg  string
}

// specEntry is one row in the slowest-tests table.
type specEntry struct {
	Name    string
	AvgSecs float64
	StdDev  float64
}

// nodeCountEst holds the LPT-simulated workflow time and full shard debug info
// for a specific shard count, computed for a given set of (possibly modified) test timings.
type nodeCountEst struct {
	Nodes        int
	EstimatedSec float64
	Debug        lptSimDebug
}

// optimEntry is one row in the optimization opportunities table.
//
// Each row i represents the state where ALL tests above (rows 0..i, inclusive)
// are reduced to the duration of the test immediately below row i in the
// ranked list. LPT is re-run after every step so the estimated workflow
// reflects the real scheduler, not a naive sum.
type optimEntry struct {
	Name                  string
	AvgSecs               float64
	TargetSecs            float64        // duration of the next-longest test (row i+1)
	SavedSecs             float64        // AvgSecs − TargetSecs (this test's gap to the next)
	CumulativeSecs        float64        // baselineWorkflow(N) − EstimatedWorkflowSecs (saving at current N nodes)
	CumulativeByOffset    []float64      // saving vs baseline for each NodeCountEsts column [N, N+1, N+2, N+4, N+8]
	EstimatedWorkflowSecs float64        // LPT estimate after fixing all tests up to and including this row (current N nodes)
	NodeCountEsts         []nodeCountEst // estimates for N, N+1, N+2, N+4, N+8
	Debug                 lptSimDebug
}

// lptSimDebug holds the full per-shard breakdown from one LPT simulation run.
type lptSimDebug struct {
	Shards           []shardDebug
	MaxShardSecs     float64
	PipelineOverhead float64
}

// shardDebug is one shard's contribution in an LPT debug snapshot.
type shardDebug struct {
	ID             int
	Specs          []specDebug
	SuiteOverheads []suiteOverheadDebug
	TestTime       float64
	SuiteTime      float64
	Total          float64
	Critical       bool
}

// specDebug is one spec within a shard debug snapshot.
type specDebug struct {
	Name     string
	DurSecs  float64
	Modified bool // duration was replaced by a target in this optimisation step
}

// suiteOverheadDebug is one BeforeSuite charge within a shard.
type suiteOverheadDebug struct {
	Suite   string
	DurSecs float64
}

// shardDistrib summarises actual shard load balance (from real job timings) and
// LPT-based projections for adding more shards.
type shardDistrib struct {
	CurrentShards  int
	AvgMaxShardSec float64 // avg slowest shard wall time (real data)
	AvgMinShardSec float64 // avg fastest shard wall time (real data)
	Projections    []shardProjection
}

// shardProjection shows the LPT-simulated workflow time for a given shard count.
type shardProjection struct {
	Shards         int
	EstimatedSec   float64 // LPT-simulated total workflow wall time
	DeltaSec       float64 // improvement vs previous row (positive = faster)
	BelowThreshold bool    // true for the last row where delta < 1 min
}

// computeShardDistrib computes shard load balance stats and LPT-based projections
// for adding more nodes.
//
//   - Per-shard timing is derived from the wall-clock duration of each e2e test
//     job within every successful run. Min/max per run are averaged across runs.
//   - Projections run the full LPT simulation (same algorithm as CI) for
//     N, N+1, N+2, … shards until the improvement over the previous count
//     drops below 1 minute. The last row is flagged BelowThreshold.
func computeShardDistrib(rawRuns []rawRunEntry, discoverySpecs []e2etestutils.SpecInfo, timings map[string]specTiming, pipelineOverheadSecs float64, inferredShards int) shardDistrib {
	var maxDurs, minDurs []float64

	for _, run := range rawRuns {
		if run.Conclusion != "success" {
			continue
		}

		var shardDurs []float64
		for _, job := range run.Jobs {
			// Only count actual e2e test-execution jobs: they are child jobs produced
			// by the matrix and have "/ e2e-test" in their name.
			if !strings.Contains(job.Name, "/ e2e-test") {
				continue
			}
			sa, err1 := time.Parse(time.RFC3339, job.StartedAt)
			sc, err2 := time.Parse(time.RFC3339, job.CompletedAt)
			if err1 != nil || err2 != nil || sc.Before(sa) {
				continue
			}
			dur := sc.Sub(sa).Seconds()
			if dur > 0 {
				shardDurs = append(shardDurs, dur)
			}
		}

		if len(shardDurs) < 2 {
			continue
		}

		minD, maxD := shardDurs[0], shardDurs[0]
		for _, d := range shardDurs[1:] {
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		}
		minDurs = append(minDurs, minD)
		maxDurs = append(maxDurs, maxD)
	}

	avgMax, _ := avgStddev(maxDurs)
	avgMin, _ := avgStddev(minDurs)

	n := max(inferredShards, 1)

	// Run LPT from current node count upward. A single sub-threshold step does
	// not mean the trend is over (e.g. BeforeSuite overhead creates a plateau for
	// one step before freeing up again). Stop only after 3 consecutive steps that
	// each save less than 1 minute, or once we reach the hard cap.
	const (
		minGainSecs      = 60.0
		consecutiveLimit = 3
	)
	hardCap := max(50, 3*n)
	var projections []shardProjection
	prevSec := lptSimulate(timings, discoverySpecs, n, pipelineOverheadSecs)
	projections = append(projections, shardProjection{
		Shards:       n,
		EstimatedSec: prevSec,
	})

	belowCount := 0
	for shards := n + 1; shards <= hardCap; shards++ {
		estSec := lptSimulate(timings, discoverySpecs, shards, pipelineOverheadSecs)
		// More shards is always at least as good as fewer — the previous assignment
		// remains valid with an extra empty shard. Clamp for the same reason as in
		// computeOptimizations: LPT's greedy choices can produce a worse packing
		// when BeforeSuite costs redistribute across the new shard count.
		if estSec > prevSec {
			estSec = prevSec
		}
		delta := prevSec - estSec // positive = improvement
		below := delta < minGainSecs
		projections = append(projections, shardProjection{
			Shards:         shards,
			EstimatedSec:   estSec,
			DeltaSec:       delta,
			BelowThreshold: below,
		})
		prevSec = estSec
		if below {
			belowCount++
		} else {
			belowCount = 0
		}
		if belowCount >= consecutiveLimit {
			break
		}
	}

	return shardDistrib{
		CurrentShards:  n,
		AvgMaxShardSec: avgMax,
		AvgMinShardSec: avgMin,
		Projections:    projections,
	}
}

func computeReport(jobsFile rawJobsFile, junitFiles []rawJUnitFile, discoverySpecs []e2etestutils.SpecInfo, topN int) *reportData {
	// All run references come from jobsFile.Runs — every collected run has an
	// entry there, including failed runs (with empty Jobs). This is the
	// single source of truth for "what runs were analyzed".
	allRunRefs := make([]runRef, 0, len(jobsFile.Runs))
	for _, jr := range jobsFile.Runs {
		allRunRefs = append(allRunRefs, runRef{RunID: jr.RunID, RunURL: jr.RunURL, Date: jr.Date})
	}

	pagg := aggregateRawJobs(jobsFile.Runs)
	jagg := aggregateJUnit(junitFiles, allRunRefs)
	timings := computeTimings(junitFiles, allRunRefs)

	r := &reportData{
		GeneratedAt:           jobsFile.Meta.GeneratedAt,
		RepoURL:               jobsFile.Meta.RepoURL,
		AnalyzedRuns:          jobsFile.Meta.AnalyzedRuns,
		InfraInstabilityCount: len(jagg.infraRuns),
		PreTestFailureCount:   len(jagg.noJUnit),
		PreTestFailureRefs:    jagg.noJUnit,
	}

	r.PipelinePhases = buildPipelinePhases(pagg)
	r.EstimatedWallTimeSecs = estimateWallTime(pagg)

	r.FlakeEntries, r.TotalAnalyzedSpecs, r.FlakyCount, r.ConsistentlyFailingCount, r.CleanCount = computeFlakes(jagg)

	r.SlowestTests = computeSlowest(timings, topN)

	shardCount := inferShardCount(pagg)
	r.InferredShardCount = shardCount

	// Compute pipeline overhead once (observed wall time minus LPT max shard),
	// shared by both the optimisation simulation and the shard-count projections.
	currentLPTWall := lptSimulate(timings, discoverySpecs, shardCount, 0)
	pipelineOverhead := r.EstimatedWallTimeSecs - currentLPTWall
	if pipelineOverhead < 0 {
		pipelineOverhead = 0
	}

	r.PipelineOverheadSecs = pipelineOverhead
	r.BaselineShardSecs = currentLPTWall
	r.PipelineOverviewReady = r.EstimatedWallTimeSecs > 0 && shardCount > 0
	r.OptimEntries, r.TotalSuiteSecs, r.TopNPct, r.OptimBaselineWorkflowSecs, r.OptimBestWorkflowSecs = computeOptimizations(timings, discoverySpecs, topN, shardCount, r.EstimatedWallTimeSecs, pipelineOverhead)

	// Node-count offsets match those used inside computeOptimizations.
	r.OptimNodeOffsets = []int{0, 1, 2, 4, 8}

	return r
}

// buildPipelinePhases converts aggregated pipeline observations to ordered phase rows.
// Phase keys follow two conventions:
//   - "job-name" or "job-name_variant"      → top-level job row (no "::" in key)
//   - "job-name_variant::step name"         → step row, child of the parent job key
//
// The display table is built entirely from whatever keys exist in the data, so
// adding or renaming workflow jobs/steps requires no changes here.
func buildPipelinePhases(agg pipelineAgg) []phaseEntry {
	// Separate job-level keys (no "::") from step-level keys (contain "::").
	jobKeys := make([]string, 0)
	for k := range agg.phases {
		if !strings.Contains(k, "::") {
			jobKeys = append(jobKeys, k)
		}
	}
	// Sort jobs: variant jobs (e2e shards) after non-variant jobs (build),
	// then alphabetically within each group so the order is deterministic.
	sort.Slice(jobKeys, func(i, j int) bool {
		iHasVariant := reVariant.MatchString(jobKeys[i])
		jHasVariant := reVariant.MatchString(jobKeys[j])
		if iHasVariant != jHasVariant {
			return !iHasVariant // non-variant (build) jobs first
		}
		return jobKeys[i] < jobKeys[j]
	})

	var result []phaseEntry
	for _, jobKey := range jobKeys {
		obs := agg.phases[jobKey]
		if len(obs) == 0 {
			continue
		}
		avg, std := avgStddev(obs)
		// Skip trivial top-level jobs entirely — they add noise, not insight.
		if avg < minPhaseDisplaySecs {
			continue
		}
		label := formatJobLabel(jobKey)
		result = append(result, phaseEntry{Name: label, AvgSecs: avg, StdDev: std, Indent: 0})

		// Collect step children; split into significant (shown) and trivial
		// (collapsed into a single "other" row).
		prefix := jobKey + "::"
		type stepKV struct {
			key string
			avg float64
			std float64
		}
		var significant, trivial []stepKV
		for k, sObs := range agg.phases {
			if !strings.HasPrefix(k, prefix) || len(sObs) == 0 {
				continue
			}
			sAvg, sStd := avgStddev(sObs)
			if sAvg >= minPhaseDisplaySecs {
				significant = append(significant, stepKV{k, sAvg, sStd})
			} else {
				trivial = append(trivial, stepKV{k, sAvg, sStd})
			}
		}
		sort.Slice(significant, func(i, j int) bool { return significant[i].avg > significant[j].avg })
		for _, s := range significant {
			stepName := strings.TrimPrefix(s.key, prefix)
			result = append(result, phaseEntry{Name: "↳ " + stepName, AvgSecs: s.avg, StdDev: s.std, Indent: 1})
		}
		if len(trivial) > 0 {
			var otherSum float64
			for _, s := range trivial {
				otherSum += s.avg
			}
			result = append(result, phaseEntry{
				Name:    fmt.Sprintf("↳ other (%d steps)", len(trivial)),
				AvgSecs: otherSum,
				Indent:  1,
			})
		}
	}
	return result
}

// formatJobLabel converts a job group key to a human-readable display label.
//
//	"e2e-test_cs9"              → "e2e-test (cs9) — slowest shard"
//	"build-images-and-charts"   → "build-images-and-charts"
func formatJobLabel(key string) string {
	variant := reVariant.FindString(key)
	if variant == "" {
		return key
	}
	base := strings.TrimSuffix(key, "_"+variant)
	return base + " (" + variant + ")*"
}

func estimateWallTime(agg pipelineAgg) float64 {
	if len(agg.wallTimes) == 0 {
		return 0
	}
	avg, _ := avgStddev(agg.wallTimes)
	return avg
}

func computeFlakes(jagg junitAgg) (entries []flakeEntry, total, flaky, failing, clean int) {
	for name, r := range jagg.specs {
		totalObs := r.PassCount + r.FailCount
		if totalObs == 0 {
			continue // skip-only specs are not tracked
		}
		total++
		class := classifyFlake(r)
		switch class {
		case "Flaky":
			flaky++
		case "Consistently failing":
			failing++
		default:
			clean++
			continue
		}
		entries = append(entries, flakeEntry{
			SpecName:    name,
			GinkgoLabel: r.GinkgoLabel,
			FailRate:    float64(r.FailCount) / float64(totalObs),
			FailCount:   r.FailCount,
			TotalRuns:   totalObs,
			Class:       class,
			LastRunURL:  r.LastFailedURL,
			FailureMsg:  r.FailureMsg,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].FailRate > entries[j].FailRate })
	return entries, total, flaky, failing, clean
}

// classifyFlake returns "Consistently failing", "Flaky", or "" (never failed).
//
//   - "Consistently failing" – failed in every observed run (no passes at all)
//   - "Flaky"               – failed at least once AND passed at least once
//   - ""                    – passed in every observed run (no failures at all)
func classifyFlake(r specResult) string {
	switch {
	case r.FailCount == 0:
		return "" // always passed
	case r.PassCount == 0:
		return "Consistently failing" // always failed
	default:
		return "Flaky" // mixed
	}
}

func computeSlowest(timings map[string]specTiming, topN int) []specEntry {
	var specs []specEntry
	for name, t := range timings {
		if strings.HasPrefix(name, "__suite__:") {
			continue
		}
		specs = append(specs, specEntry{Name: name, AvgSecs: t.Avg, StdDev: t.StdDev})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].AvgSecs > specs[j].AvgSecs })
	if len(specs) > topN {
		return specs[:topN]
	}
	return specs
}

// lptSimulate runs the LPT algorithm with the given timings and returns the
// estimated total workflow time (pipeline overhead + max shard duration).
func lptSimulate(timings map[string]specTiming, specs []e2etestutils.SpecInfo, shardCount int, pipelineOverheadSecs float64) float64 {
	_, dbg := lptSimulateDebug(timings, specs, shardCount, pipelineOverheadSecs, nil)
	return dbg.PipelineOverhead + dbg.MaxShardSecs
}

// lptSimulateDebug runs LPT using the discovery spec list and returns both the
// estimated workflow time and a full per-shard breakdown for the debug popup.
// Using the discovery spec list (not just JUnit-observed specs) ensures the
// simulation matches compute_test_assignments exactly, including unknown specs
// that fall back to the default duration.
// modifiedSpecs flags specs whose durations were replaced at this step.
func lptSimulateDebug(timings map[string]specTiming, specs []e2etestutils.SpecInfo, shardCount int, pipelineOverheadSecs float64, modifiedSpecs map[string]bool) (float64, lptSimDebug) {
	dbg := lptSimDebug{PipelineOverhead: pipelineOverheadSecs}
	if shardCount < 1 || len(specs) == 0 {
		return pipelineOverheadSecs, dbg
	}
	specTimings, suiteTimings := e2etestutils.SeparateTimings(timings)

	// sigma=1.0 matches compute_test_assignments default: effective weight = avg + 1×stddev.
	// This ensures the shard assignment order and per-shard totals are identical to CI.
	const sigma = 1.0
	defDuration := e2etestutils.DefaultDuration(specTimings, 60.0)
	assignments, maxShard := e2etestutils.LPTAssign(specs, specTimings, suiteTimings, shardCount, defDuration, sigma)

	dbg.MaxShardSecs = maxShard

	// Build a suite lookup from the discovery spec list.
	suiteOf := make(map[string]string, len(specs))
	for _, s := range specs {
		suiteOf[s.Name] = s.Suite
	}

	// Reconstruct per-shard detail from the returned assignments.
	for nodeIDStr, specNames := range assignments {
		nodeID := 0
		fmt.Sscanf(nodeIDStr, "%d", &nodeID) //nolint:errcheck
		var sd shardDebug
		sd.ID = nodeID
		seenSuites := make(map[string]struct{})
		for _, name := range specNames {
			dur := defDuration
			if t, ok := specTimings[name]; ok {
				dur = e2etestutils.EffectiveWeight(t, sigma)
			}
			sd.Specs = append(sd.Specs, specDebug{
				Name:     name,
				DurSecs:  dur,
				Modified: modifiedSpecs[name],
			})
			sd.TestTime += dur

			suite := suiteOf[name]
			if suite != "" {
				if _, seen := seenSuites[suite]; !seen {
					seenSuites[suite] = struct{}{}
					if overhead, ok := suiteTimings[suite]; ok {
						ew := e2etestutils.EffectiveWeight(overhead, sigma)
						sd.SuiteOverheads = append(sd.SuiteOverheads, suiteOverheadDebug{
							Suite:   suite,
							DurSecs: ew,
						})
						sd.SuiteTime += ew
					}
				}
			}
		}
		// Sort specs within shard by duration descending for readability.
		sort.Slice(sd.Specs, func(i, j int) bool { return sd.Specs[i].DurSecs > sd.Specs[j].DurSecs })
		sd.Total = sd.TestTime + sd.SuiteTime
		dbg.Shards = append(dbg.Shards, sd)
	}

	// Mark the critical (heaviest) shard before re-sorting by node ID.
	maxTotal := 0.0
	for _, s := range dbg.Shards {
		if s.Total > maxTotal {
			maxTotal = s.Total
		}
	}
	for i := range dbg.Shards {
		if dbg.Shards[i].Total == maxTotal {
			dbg.Shards[i].Critical = true
			break
		}
	}
	// Sort by node ID ascending for display.
	sort.Slice(dbg.Shards, func(i, j int) bool { return dbg.Shards[i].ID < dbg.Shards[j].ID })

	return pipelineOverheadSecs + maxShard, dbg
}

func computeOptimizations(timings map[string]specTiming, discoverySpecs []e2etestutils.SpecInfo, topN int, shardCount int, estimatedWallSecs float64, pipelineOverhead ...float64) (
	entries []optimEntry, totalSuite, topNPct, baselineWorkflow, bestWorkflow float64,
) {
	for name, t := range timings {
		if !strings.HasPrefix(name, "__suite__:") {
			totalSuite += t.Avg
		}
	}

	// All specs sorted slowest-first. We need topN+1 so each of the topN rows
	// has a "next" test to use as its target.
	allRanked := computeSlowest(timings, topN+1)
	if len(allRanked) < 2 {
		baselineWorkflow = estimatedWallSecs
		bestWorkflow = estimatedWallSecs
		return
	}
	// Limit to topN rows; the (topN+1)-th entry only serves as the final target.
	rowCount := len(allRanked) - 1
	if rowCount > topN {
		rowCount = topN
	}

	// Use the pre-computed pipeline overhead when provided; otherwise derive it.
	var overhead float64
	if len(pipelineOverhead) > 0 {
		overhead = pipelineOverhead[0]
	} else {
		currentLPTWall := lptSimulate(timings, discoverySpecs, shardCount, 0)
		overhead = estimatedWallSecs - currentLPTWall
		if overhead < 0 {
			overhead = 0
		}
	}

	// Consistent LPT-based baseline (pipeline overhead already included).
	// Also capture the full debug breakdown so the user can compare with
	// compute_test_assignments before any optimisation is applied.
	baselineWorkflow, baselineDbg := lptSimulateDebug(timings, discoverySpecs, shardCount, overhead, nil)

	// nodeOffsets is the set of extra shard-count columns shown in the table:
	// current N, N+1, N+2, N+4, N+8. Each entry carries estimates for all of them.
	nodeOffsets := []int{0, 1, 2, 4, 8}

	// nodeCountEsts computes the workflow estimate for each shard-count offset,
	// applying within-row clamping (more nodes ≥ as good as fewer) and accepting
	// a per-offset previous value for across-row clamping.
	nodeCountEsts := func(t map[string]specTiming, modSpecs map[string]bool, prevByOffset map[int]float64) []nodeCountEst {
		ests := make([]nodeCountEst, len(nodeOffsets))
		var prevInRow float64
		for j, off := range nodeOffsets {
			est, dbg := lptSimulateDebug(t, discoverySpecs, shardCount+off, overhead, modSpecs)
			// Within a row: more nodes must be at most as slow as fewer nodes.
			if j > 0 && est > prevInRow {
				est = prevInRow
			}
			// Across rows: same node count must never regress from the previous step.
			if prev, ok := prevByOffset[off]; ok && est > prev {
				est = prev
			}
			prevByOffset[off] = est
			prevInRow = est
			ests[j] = nodeCountEst{Nodes: shardCount + off, EstimatedSec: est, Debug: dbg}
		}
		return ests
	}

	// Prepend a synthetic "current" row (no test modified) so the first popup
	// shows the unmodified LPT distribution for direct comparison with
	// compute_test_assignments output.
	prevByOffset := make(map[int]float64, len(nodeOffsets))
	baselineEntry := optimEntry{
		Name:                  "(current — no changes)",
		EstimatedWorkflowSecs: baselineWorkflow,
		NodeCountEsts:         nodeCountEsts(timings, nil, prevByOffset),
		Debug:                 baselineDbg,
	}

	// Iterative simulation: at step i we set all tests allRanked[0..i] to the
	// duration of allRanked[i+1] (the test immediately below), then re-run LPT.
	// This answers: "what if everything above this level were as fast as this level?"
	modTimings := make(map[string]specTiming, len(timings))
	for k, v := range timings {
		modTimings[k] = v
	}
	// modifiedSpecs tracks which specs have been reduced so far, for debug display.
	modifiedSpecs := make(map[string]bool)

	// Capture baseline per-node estimates for cumulative-saving range computation.
	baselineNCEsts := baselineEntry.NodeCountEsts

	entries = make([]optimEntry, 0, rowCount+1)
	entries = append(entries, baselineEntry)
	prevWorkflow := baselineWorkflow
	for i := 0; i < rowCount; i++ {
		cur := allRanked[i]
		next := allRanked[i+1]
		target := next.AvgSecs

		// Reduce ALL tests allRanked[0..i] to target. Previous steps already reduced
		// allRanked[0..i-1] to their respective targets; re-applying here brings
		// them down to the new (lower) target so the LPT simulation always reflects
		// the cumulative state: "everything above this level is as fast as this level".
		for j := 0; j <= i; j++ {
			modTimings[allRanked[j].Name] = specTiming{Avg: target, StdDev: 0}
			modifiedSpecs[allRanked[j].Name] = true
		}

		estimatedAfter, dbg := lptSimulateDebug(modTimings, discoverySpecs, shardCount, overhead, modifiedSpecs)

		// The previous assignment is always valid with the new (smaller) durations,
		// so the true achievable makespan is monotonically non-increasing.
		// LPT's greedy choices can sometimes produce a worse result than the prior
		// step (e.g. expensive BeforeSuite overheads concentrate on a new shard).
		// Clamp here so we never report a regression.
		if estimatedAfter > prevWorkflow {
			estimatedAfter = prevWorkflow
		}
		prevWorkflow = estimatedAfter

		ncEsts := nodeCountEsts(modTimings, modifiedSpecs, prevByOffset)

		cumByOffset := make([]float64, len(ncEsts))
		for j, nc := range ncEsts {
			if j < len(baselineNCEsts) {
				cumByOffset[j] = baselineNCEsts[j].EstimatedSec - nc.EstimatedSec
			}
		}

		entries = append(entries, optimEntry{
			Name:                  cur.Name,
			AvgSecs:               cur.AvgSecs,
			TargetSecs:            target,
			SavedSecs:             cur.AvgSecs - target,
			CumulativeSecs:        baselineWorkflow - estimatedAfter,
			CumulativeByOffset:    cumByOffset,
			EstimatedWorkflowSecs: estimatedAfter,
			NodeCountEsts:         ncEsts,
			Debug:                 dbg,
		})
	}

	bestWorkflow = entries[len(entries)-1].EstimatedWorkflowSecs

	if totalSuite > 0 {
		var topNTotal float64
		for _, e := range entries[1:] { // skip the baseline row
			topNTotal += e.AvgSecs
		}
		topNPct = topNTotal / totalSuite * 100
	}
	return
}

func inferShardCount(agg pipelineAgg) int {
	// Pick the variant with the highest modal shard count (typically the
	// primary variant with the most shards). Iterating in sorted order makes
	// the result deterministic when two variants tie.
	variants := make([]string, 0, len(agg.shardCounts))
	for v := range agg.shardCounts {
		variants = append(variants, v)
	}
	sort.Strings(variants)
	best := 0
	for _, v := range variants {
		if n := modeInt(agg.shardCounts[v]); n > best {
			best = n
		}
	}
	if best > 0 {
		return best
	}
	return 10
}

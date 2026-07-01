package main

import (
	"sort"
	"time"
)

// junitAgg is the in-memory result of aggregating raw run data.
// It is always computed from raw-junit.json and never persisted.
type junitAgg struct {
	specs     map[string]specResult
	noJUnit   []runRef
	infraRuns []runRef
}

// specResult tracks aggregate pass/fail counts across analyzed runs.
type specResult struct {
	PassCount       int
	FailCount       int
	SkipCount       int
	LastFailedRunID int64
	LastFailedURL   string
	FailureMsg      string
}

// aggregateRuns builds per-spec pass/fail counts, detects infra instability
// runs, and records runs that produced no JUnit output.
//
// Infra instability detection: if >infraInstabilityThreshold of the executed
// specs fail in a single run, the entire run is excluded from per-spec counts.
// This prevents environment failures from inflating individual spec flake rates.
func aggregateRuns(runs []rawRunData) junitAgg {
	agg := junitAgg{
		specs: make(map[string]specResult),
	}

	// Runs are stored newest-first from collect; process in that order so
	// LastFailedURL records the most recent failure URL for each spec.
	for _, run := range runs {
		// Collection failures are excluded entirely; they are not "no JUnit".
		if run.CollectError != "" {
			continue
		}
		ref := runRef{RunID: run.RunID, RunURL: run.RunURL, Date: run.Date}

		if len(run.Specs) == 0 {
			agg.noJUnit = append(agg.noJUnit, ref)
			continue
		}

		// Infra instability check: only count executed (non-skipped) specs.
		executed := 0
		failures := 0
		for _, s := range run.Specs {
			if s.Skipped {
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

		for name, s := range run.Specs {
			existing := agg.specs[name]
			switch {
			case s.Skipped:
				existing.SkipCount++
			case s.Passed:
				existing.PassCount++
			default:
				existing.FailCount++
				if existing.LastFailedRunID == 0 {
					existing.LastFailedRunID = run.RunID
					existing.LastFailedURL = run.RunURL
				}
				if s.FailureMsg != "" && existing.FailureMsg == "" {
					existing.FailureMsg = s.FailureMsg
				}
			}
			agg.specs[name] = existing
		}
	}
	return agg
}

// computeTimings derives per-spec avg duration and stddev from raw run data.
// Infra instability runs are excluded. Skipped specs and zero-duration entries
// are ignored.
func computeTimings(runs []rawRunData) map[string]specTiming {
	allObs := make(map[string][]float64)
	for _, run := range runs {
		if len(run.Specs) == 0 {
			continue
		}
		// Skip infra instability runs.
		executed := 0
		failures := 0
		for _, s := range run.Specs {
			if s.Skipped {
				continue
			}
			executed++
			if !s.Passed {
				failures++
			}
		}
		if executed > 0 && float64(failures)/float64(executed) > infraInstabilityThreshold {
			continue
		}
		for name, s := range run.Specs {
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

// specTiming holds the average and standard deviation of a spec's duration.
type specTiming struct {
	Avg    float64
	StdDev float64
}

// reportData is the output of computeReport. It contains only plain Go values
// — no template.HTML, no SVG. The render layer wraps it in a renderContext to
// add the generated charts before executing the HTML template.
type reportData struct {
	Title        string
	GeneratedAt  string
	RepoURL      string
	Workflow     string
	AnalyzedRuns int

	// Section: Flaky tests.
	FlakeEntries             []flakeEntry
	InfraInstabilityCount    int
	NoJUnitCount             int
	CollectErrorCount        int
	TotalAnalyzedSpecs       int
	FlakyCount               int
	ConsistentlyFailingCount int
	CleanCount               int

	// Section: Slowest tests.
	SlowestTests []specEntry

	// Section: Per-run trend.
	TrendEntries []trendEntry
}

// flakeEntry is one row in the flaky tests table.
type flakeEntry struct {
	SpecName   string
	FailRate   float64
	FailCount  int
	TotalRuns  int
	Class      string // "Flaky" or "Consistently failing"
	LastRunURL string
	FailureMsg string
}

// specEntry is one row in the slowest-tests table.
type specEntry struct {
	Name    string
	AvgSecs float64
	StdDev  float64
}

// trendEntry is one row in the per-run trend table.
type trendEntry struct {
	RunID      int64
	RunURL     string
	Date       string
	Conclusion string
	Passed     int
	Failed     int
	Skipped    int
	Total      int
}

func computeFlakes(agg junitAgg, topN int) (flakes []flakeEntry, totalSpecs, flakyCount, consistentCount, cleanCount int) {
	// Classify each spec that appeared at least once (pass or fail; skip-only excluded).
	for name, sr := range agg.specs {
		total := sr.PassCount + sr.FailCount
		if total == 0 {
			// Only ever skipped — not interesting.
			continue
		}
		totalSpecs++
		if sr.FailCount == 0 {
			cleanCount++
			continue
		}
		failRate := float64(sr.FailCount) / float64(total)
		class := "Flaky"
		if sr.PassCount == 0 {
			class = "Consistently failing"
			consistentCount++
		} else {
			flakyCount++
		}
		flakes = append(flakes, flakeEntry{
			SpecName:   name,
			FailRate:   failRate,
			FailCount:  sr.FailCount,
			TotalRuns:  total,
			Class:      class,
			LastRunURL: sr.LastFailedURL,
			FailureMsg: sr.FailureMsg,
		})
	}
	// Sort: consistently failing first, then by fail rate descending.
	sort.Slice(flakes, func(i, j int) bool {
		ci := flakes[i].Class == "Consistently failing"
		cj := flakes[j].Class == "Consistently failing"
		if ci != cj {
			return ci
		}
		return flakes[i].FailRate > flakes[j].FailRate
	})
	if topN > 0 && len(flakes) > topN {
		flakes = flakes[:topN]
	}
	return flakes, totalSpecs, flakyCount, consistentCount, cleanCount
}

func computeSlowest(timings map[string]specTiming, topN int) []specEntry {
	entries := make([]specEntry, 0, len(timings))
	for name, t := range timings {
		entries = append(entries, specEntry{Name: name, AvgSecs: t.Avg, StdDev: t.StdDev})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].AvgSecs > entries[j].AvgSecs
	})
	if topN > 0 && len(entries) > topN {
		entries = entries[:topN]
	}
	return entries
}

func computeCollectErrors(runs []rawRunData) int {
	n := 0
	for _, run := range runs {
		if run.CollectError != "" {
			n++
		}
	}
	return n
}

func computeTrend(runs []rawRunData) []trendEntry {
	trend := make([]trendEntry, 0, len(runs))
	for _, run := range runs {
		if run.CollectError != "" {
			continue
		}
		entry := trendEntry{
			RunID:      run.RunID,
			RunURL:     run.RunURL,
			Date:       run.Date,
			Conclusion: run.Conclusion,
		}
		for _, s := range run.Specs {
			switch {
			case s.Skipped:
				entry.Skipped++
			case s.Passed:
				entry.Passed++
			default:
				entry.Failed++
			}
		}
		entry.Total = entry.Passed + entry.Failed + entry.Skipped
		trend = append(trend, entry)
	}
	// Reverse to chronological order (oldest first) for the chart.
	for i, j := 0, len(trend)-1; i < j; i, j = i+1, j-1 {
		trend[i], trend[j] = trend[j], trend[i]
	}
	return trend
}

func computeReport(raw rawFile, title string, topN int) *reportData {
	agg := aggregateRuns(raw.Runs)
	timings := computeTimings(raw.Runs)

	flakes, totalSpecs, flakyCount, consistentCount, cleanCount := computeFlakes(agg, topN)
	slowest := computeSlowest(timings, topN)
	trend := computeTrend(raw.Runs)
	collectErrors := computeCollectErrors(raw.Runs)

	generatedAt := raw.Meta.GeneratedAt
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	return &reportData{
		Title:                    title,
		GeneratedAt:              generatedAt,
		RepoURL:                  raw.Meta.RepoURL,
		Workflow:                 raw.Meta.Workflow,
		AnalyzedRuns:             raw.Meta.AnalyzedRuns,
		FlakeEntries:             flakes,
		InfraInstabilityCount:    len(agg.infraRuns),
		NoJUnitCount:             len(agg.noJUnit),
		CollectErrorCount:        collectErrors,
		TotalAnalyzedSpecs:       totalSpecs,
		FlakyCount:               flakyCount,
		ConsistentlyFailingCount: consistentCount,
		CleanCount:               cleanCount,
		SlowestTests:             slowest,
		TrendEntries:             trend,
	}
}

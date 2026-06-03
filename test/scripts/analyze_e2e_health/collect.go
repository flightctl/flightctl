package main

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
)

// ginkgoLabelRe matches the first numeric Ginkgo label in a test case name.
var ginkgoLabelRe = regexp.MustCompile(`\[(\d{4,6})[,\]]`)

type junitTestSuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	TestSuites []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Time      float64    `xml:"time,attr"`
	Skipped   []struct{} `xml:"skipped"`
	Failure   []struct {
		Message string `xml:"message,attr"`
		Body    string `xml:",chardata"`
	} `xml:"failure"`
}

// junitResult holds the parsed data we need from a single test case.
type junitResult struct {
	SpecName    string
	Suite       string // Ginkgo suite description (classname in JUnit XML)
	GinkgoLabel string
	Passed      bool
	Skipped     bool
	DurationSec float64
	FailureMsg  string
}

// runSummary is a lightweight run descriptor used during collection.
type runSummary struct {
	id         int64
	url        string
	date       string
	conclusion string
	startedAt  time.Time
}

func collectHealthData(ctx context.Context, client *github.Client, owner, repo, workflow string, nRuns int) (rawJobsFile, []rawJUnitFile, error) {
	meta := collectMeta{
		Branch:      defaultBranch,
		Workflow:    workflow,
		RepoURL:     buildRepoURL(owner, repo),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	runs, err := listCompletedRuns(ctx, client, owner, repo, workflow, nRuns)
	if err != nil {
		return rawJobsFile{}, nil, fmt.Errorf("list runs: %w", err)
	}
	if len(runs) == 0 {
		fmt.Println("No completed runs found.")
		meta.AnalyzedRuns = 0
		return rawJobsFile{Meta: meta}, nil, nil
	}
	fmt.Printf("Found %d completed run(s)\n", len(runs))

	// Every run gets an entry in Runs — failed runs have empty Jobs so that
	// aggregateJUnit can detect "run had no JUnit" for any run ID.
	var jobRuns []rawRunEntry
	var junitFiles []rawJUnitFile
	for _, run := range runs {
		fmt.Printf("  Processing run %d (%s, %s)...\n", run.id, run.conclusion, run.date)
		rawJob, shards, err := processRun(ctx, client, owner, repo, run)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: %v\n", err)
		}
		if rawJob != nil {
			jobRuns = append(jobRuns, *rawJob)
		} else {
			// Failed or skipped run: add a stub entry so the run ID is tracked.
			jobRuns = append(jobRuns, rawRunEntry{
				RunID:      run.id,
				RunURL:     run.url,
				Date:       run.date,
				StartedAt:  run.startedAt.UTC().Format(time.RFC3339),
				Conclusion: run.conclusion,
			})
		}
		junitFiles = append(junitFiles, shards...)
	}
	meta.AnalyzedRuns = len(runs)
	return rawJobsFile{Meta: meta, Runs: jobRuns}, junitFiles, nil
}

func listCompletedRuns(ctx context.Context, client *github.Client, owner, repo, workflow string, limit int) ([]runSummary, error) {
	opts := &github.ListWorkflowRunsOptions{
		Branch:      defaultBranch,
		ListOptions: github.ListOptions{PerPage: limit},
	}
	runs, _, err := client.Actions.ListWorkflowRunsByFileName(ctx, owner, repo, workflow, opts)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	var result []runSummary
	for _, r := range runs.WorkflowRuns {
		conclusion := r.GetConclusion()
		if conclusion != "success" && conclusion != "failure" {
			continue
		}
		result = append(result, runSummary{
			id:         r.GetID(),
			url:        r.GetHTMLURL(),
			date:       r.GetCreatedAt().Format("2006-01-02"),
			conclusion: conclusion,
			startedAt:  r.GetRunStartedAt().Time,
		})
	}
	return result, nil
}

// processRun handles one workflow run: it fetches raw job timings (successful
// runs only) and downloads + parses JUnit XML artifacts. Each XML file becomes
// its own rawJUnitFile entry — no shard merging or aggregation is performed
// here. All logic is deferred to compute.go so this is pure data collection.
func processRun(ctx context.Context, client *github.Client, owner, repo string, run runSummary) (*rawRunEntry, []rawJUnitFile, error) {
	// Only fetch raw job timings for successful runs — failed runs abort early
	// and would skew pipeline phase averages downward.
	var rawJob *rawRunEntry
	if run.conclusion == "success" {
		r, err := fetchRawJobs(ctx, client, owner, repo, run)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: fetch raw jobs: %v\n", err)
		} else {
			rawJob = r
		}
	}

	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("e2e-health-%d-*", run.id))
	if err != nil {
		return rawJob, nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	found, err := downloadJUnitArtifacts(ctx, client, owner, repo, run.id, tmpDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    Warning: download JUnit: %v\n", err)
	}
	if !found {
		return rawJob, nil, nil
	}

	// Parse each XML file independently; one rawJUnitFile per shard artifact.
	var shards []rawJUnitFile
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		results, err := parseJUnitFile(filepath.Join(tmpDir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: parse %s: %v\n", e.Name(), err)
			continue
		}
		shard := rawJUnitFile{
			RunID:  run.id,
			RunURL: run.url,
			Date:   run.date,
			Specs:  make(map[string]rawSpecResult, len(results)),
		}
		for _, r := range results {
			shard.Specs[r.SpecName] = rawSpecResult{
				Passed:      r.Passed,
				Skipped:     r.Skipped,
				DurationSec: r.DurationSec,
				FailureMsg:  truncateMsg(r.FailureMsg, 200),
				GinkgoLabel: r.GinkgoLabel,
				Suite:       r.Suite,
			}
		}
		shards = append(shards, shard)
	}
	return rawJob, shards, nil
}

func downloadJUnitArtifacts(ctx context.Context, client *github.Client, owner, repo string, runID int64, destDir string) (bool, error) {
	var artifacts []*github.Artifact
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := client.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, opts)
		if err != nil {
			return false, fmt.Errorf("list artifacts: %w", err)
		}
		for _, a := range page.Artifacts {
			if strings.HasPrefix(a.GetName(), "junit-results-") {
				artifacts = append(artifacts, a)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if len(artifacts) == 0 {
		return false, nil
	}
	for _, a := range artifacts {
		if err := downloadAndExtract(ctx, client, owner, repo, a.GetID(), destDir); err != nil {
			fmt.Fprintf(os.Stderr, "      Warning: download %s: %v\n", a.GetName(), err)
		}
	}
	return true, nil
}

func downloadAndExtract(ctx context.Context, client *github.Client, owner, repo string, artifactID int64, destDir string) error {
	redirectURL, _, err := client.Actions.DownloadArtifact(ctx, owner, repo, artifactID, 1)
	if err != nil {
		return fmt.Errorf("get download URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirectURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp("", "artifact-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, fmt.Sprintf("%d_%s", artifactID, filepath.Base(f.Name)))
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func parseJUnitFile(path string) ([]junitResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var suites junitTestSuites
	if err := xml.Unmarshal(data, &suites); err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}
	var results []junitResult
	for _, suite := range suites.TestSuites {
		// Determine suite name: prefer the suite-level name attr; fall back to
		// classname of the first testcase (Ginkgo sets classname = suite description).
		suiteName := suite.Name
		if suiteName == "" && len(suite.TestCases) > 0 {
			suiteName = suite.TestCases[0].ClassName
		}

		for _, tc := range suite.TestCases {
			// [BeforeSuite] carries per-suite setup overhead used by the LPT simulator.
			// Store it with the "__suite__:SuiteName" key so computeTimings picks it up.
			if tc.Name == "[BeforeSuite]" {
				name := tc.ClassName
				if name == "" {
					name = suiteName
				}
				if name != "" && tc.Time > 0 {
					results = append(results, junitResult{
						SpecName:    suiteOverheadPrefix + name,
						Suite:       name,
						Passed:      true,
						DurationSec: tc.Time,
					})
				}
				continue
			}

			specName := junitSpecName(tc.Name)
			if specName == "" {
				continue
			}
			// Use classname as the suite identifier (matches compute_test_assignments).
			suite := tc.ClassName
			if suite == "" {
				suite = suiteName
			}
			r := junitResult{
				SpecName:    specName,
				Suite:       suite,
				GinkgoLabel: extractGinkgoLabel(tc.Name),
				Skipped:     len(tc.Skipped) > 0,
				Passed:      len(tc.Failure) == 0 && len(tc.Skipped) == 0,
				DurationSec: tc.Time,
			}
			if len(tc.Failure) > 0 {
				msg := tc.Failure[0].Message
				if msg == "" {
					msg = strings.TrimSpace(tc.Failure[0].Body)
				}
				r.FailureMsg = msg
			}
			results = append(results, r)
		}
	}
	return results, nil
}

// junitSpecName extracts the plain spec name from a Ginkgo JUnit testcase name.
// It strips the "[It] " prefix and the trailing " [labels]" group so the key
// matches what compute_test_assignments produces.
func junitSpecName(name string) string {
	const itPrefix = "[It] "
	if !strings.HasPrefix(name, itPrefix) {
		return ""
	}
	name = strings.TrimPrefix(name, itPrefix)
	if idx := strings.LastIndex(name, " ["); idx > 0 && strings.HasSuffix(name, "]") {
		name = name[:idx]
	}
	return name
}

// extractGinkgoLabel pulls the first numeric Ginkgo label (4-6 digits) from a
// raw JUnit test case name, e.g. "[It] Suite Spec [78753, sanity]" → "78753".
func extractGinkgoLabel(junitName string) string {
	m := ginkgoLabelRe.FindStringSubmatch(junitName)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func truncateMsg(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// fetchRawJobs queries the GitHub Jobs API for one run and returns the raw
// job and step data without any aggregation. The result is stored to disk so
// that aggregation (aggregate.go) can be re-run without re-fetching from the API.
func fetchRawJobs(ctx context.Context, client *github.Client, owner, repo string, run runSummary) (*rawRunEntry, error) {
	var allJobs []*github.WorkflowJob
	opts := &github.ListWorkflowJobsOptions{
		Filter:      "all",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		jobs, resp, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, run.id, opts)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		allJobs = append(allJobs, jobs.Jobs...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	entry := &rawRunEntry{
		RunID:      run.id,
		RunURL:     run.url,
		Date:       run.date,
		StartedAt:  run.startedAt.UTC().Format(time.RFC3339),
		Conclusion: run.conclusion,
	}

	for _, job := range allJobs {
		sa := job.GetStartedAt().Time
		sc := job.GetCompletedAt().Time
		if sa.IsZero() || sc.IsZero() {
			continue
		}
		j := rawJobEntry{
			Name:        job.GetName(),
			StartedAt:   sa.UTC().Format(time.RFC3339),
			CompletedAt: sc.UTC().Format(time.RFC3339),
		}
		for _, step := range job.Steps {
			ssa := step.GetStartedAt().Time
			ssc := step.GetCompletedAt().Time
			if ssa.IsZero() || ssc.IsZero() {
				continue
			}
			dur := ssc.Sub(ssa).Seconds()
			if dur <= 0 {
				continue
			}
			j.Steps = append(j.Steps, rawStep{
				Name:        step.GetName(),
				DurationSec: dur,
			})
		}
		entry.Jobs = append(entry.Jobs, j)
	}
	return entry, nil
}

func avgStddev(values []float64) (avg, stddev float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	avg = sum / float64(len(values))
	if len(values) < 2 {
		return avg, 0
	}
	var variance float64
	for _, v := range values {
		d := v - avg
		variance += d * d
	}
	return avg, math.Sqrt(variance / float64(len(values)))
}

func modeInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	counts := map[int]int{}
	for _, v := range values {
		counts[v]++
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	best, bestCount := 0, 0
	for _, k := range keys {
		if counts[k] > bestCount {
			best = k
			bestCount = counts[k]
		}
	}
	return best
}

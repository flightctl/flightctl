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
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
)

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

func collectHealthData(ctx context.Context, client *github.Client, owner, repo, workflow, artifactName, title string, nRuns int) (rawFile, error) {
	meta := collectMeta{
		Branch:       defaultBranch,
		Workflow:     workflow,
		ArtifactName: artifactName,
		Title:        title,
		RepoURL:      buildRepoURL(owner, repo),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	runs, err := listCompletedRuns(ctx, client, owner, repo, workflow, nRuns)
	if err != nil {
		return rawFile{}, fmt.Errorf("list runs: %w", err)
	}
	if len(runs) == 0 {
		fmt.Println("No completed runs found.")
		meta.AnalyzedRuns = 0
		return rawFile{Meta: meta}, nil
	}
	fmt.Printf("Found %d completed run(s)\n", len(runs))

	var runData []rawRunData
	for _, run := range runs {
		fmt.Printf("  Processing run %d (%s, %s)...\n", run.id, run.conclusion, run.date)
		specs, err := processRun(ctx, client, owner, repo, run, artifactName)
		rd := rawRunData{
			RunID:      run.id,
			RunURL:     run.url,
			Date:       run.date,
			Conclusion: run.conclusion,
			Specs:      specs,
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ERROR: run %d: %v\n", run.id, err)
			rd.CollectError = err.Error()
		}
		runData = append(runData, rd)
	}
	meta.AnalyzedRuns = len(runs)
	return rawFile{Meta: meta, Runs: runData}, nil
}

func listCompletedRuns(ctx context.Context, client *github.Client, owner, repo, workflow string, limit int) ([]runSummary, error) {
	opts := &github.ListWorkflowRunsOptions{
		Branch:      defaultBranch,
		Status:      "completed",
		ListOptions: github.ListOptions{PerPage: limit},
	}
	runs, _, err := client.Actions.ListWorkflowRunsByFileName(ctx, owner, repo, workflow, opts)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	var result []runSummary
	for _, r := range runs.WorkflowRuns {
		result = append(result, runSummary{
			id:         r.GetID(),
			url:        r.GetHTMLURL(),
			date:       r.GetCreatedAt().Format("2006-01-02"),
			conclusion: r.GetConclusion(),
			startedAt:  r.GetRunStartedAt().Time,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].startedAt.After(result[j].startedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// processRun downloads the named JUnit artifact for one workflow run and
// returns the parsed spec results. Returns nil specs (not an error) when no
// matching artifact exists (tests were skipped or run failed before tests ran).
func processRun(ctx context.Context, client *github.Client, owner, repo string, run runSummary, artifactName string) (map[string]rawSpecResult, error) {
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("test-health-%d-*", run.id))
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	found, err := downloadArtifact(ctx, client, owner, repo, run.id, artifactName, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("download artifact: %w", err)
	}
	if !found {
		return nil, nil
	}

	// Parse all XML files found in the artifact (usually just one).
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read temp dir: %w", err)
	}

	merged := make(map[string]rawSpecResult)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		results, err := parseJUnitFile(filepath.Join(tmpDir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: parse %s: %v\n", e.Name(), err)
			continue
		}
		for _, r := range results {
			existing, seen := merged[r.SpecName]
			if !seen {
				merged[r.SpecName] = rawSpecResult{
					Passed:      r.Passed,
					Skipped:     r.Skipped,
					DurationSec: r.DurationSec,
					FailureMsg:  truncateMsg(r.FailureMsg, 200),
				}
				continue
			}
			// Failure always wins over pass or skip.
			if !r.Passed && !r.Skipped {
				existing.Passed = false
				existing.Skipped = false
				if r.DurationSec > 0 && existing.DurationSec == 0 {
					existing.DurationSec = r.DurationSec
				}
				if r.FailureMsg != "" && existing.FailureMsg == "" {
					existing.FailureMsg = truncateMsg(r.FailureMsg, 200)
				}
				merged[r.SpecName] = existing
			}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// downloadArtifact fetches the artifact with the exact given name for runID,
// extracts its contents to destDir, and reports whether a matching artifact was found.
func downloadArtifact(ctx context.Context, client *github.Client, owner, repo string, runID int64, artifactName, destDir string) (bool, error) {
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := client.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, opts)
		if err != nil {
			return false, fmt.Errorf("list artifacts: %w", err)
		}
		for _, a := range page.Artifacts {
			if a.GetName() == artifactName {
				if err := downloadAndExtract(ctx, client, owner, repo, a.GetID(), destDir); err != nil {
					return true, fmt.Errorf("extract artifact: %w", err)
				}
				return true, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return false, nil
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
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Do(req)
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
		suiteName := suite.Name
		if suiteName == "" && len(suite.TestCases) > 0 {
			suiteName = suite.TestCases[0].ClassName
		}

		for _, tc := range suite.TestCases {
			// Skip BeforeSuite overhead entries — they are not test specs.
			if tc.Name == "[BeforeSuite]" || tc.Name == "[AfterSuite]" {
				continue
			}

			specName := specNameFromCase(tc, suiteName)
			if specName == "" {
				continue
			}
			r := junitResult{
				SpecName:    specName,
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

// specNameFromCase derives a stable, human-readable spec key from a JUnit test case.
//
// Ginkgo format:   "[It] Suite Spec [labels]" → "Suite Spec"
// Go test format:  classname="pkg/path", name="TestFoo/sub" → "pkg/path/TestFoo/sub"
//
// For Go tests the full module prefix (e.g. "github.com/flightctl/flightctl/") is
// stripped so keys are reasonably short while still unique within the repo.
func specNameFromCase(tc junitTestCase, suiteName string) string {
	const itPrefix = "[It] "
	if strings.HasPrefix(tc.Name, itPrefix) {
		name := strings.TrimPrefix(tc.Name, itPrefix)
		// Strip trailing Ginkgo label group " [label, label]".
		if idx := strings.LastIndex(name, " ["); idx > 0 && strings.HasSuffix(name, "]") {
			name = name[:idx]
		}
		return name
	}

	// Standard Go test: build "short/pkg/TestFoo/subtest".
	cn := tc.ClassName
	if cn == "" {
		cn = suiteName
	}
	// Strip the module path prefix so display names are concise.
	if idx := strings.Index(cn, "/"); idx >= 0 {
		// Drop everything up to the first path segment that looks like a
		// Go source directory (heuristic: find "internal/", "pkg/", "api/", "cmd/").
		for _, anchor := range []string{"/internal/", "/pkg/", "/api/", "/cmd/"} {
			if i := strings.Index(cn, anchor); i >= 0 {
				cn = cn[i+1:]
				break
			}
		}
	}
	if cn != "" {
		return cn + "/" + tc.Name
	}
	return tc.Name
}

func truncateMsg(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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

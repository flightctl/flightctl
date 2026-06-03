// Command analyze_e2e_health produces a unified weekly HTML health report for
// the e2e CI pipeline, covering flaky tests, pipeline timing, spec timing
// intelligence, and optimization opportunities.
//
// Data flow:
//
//  1. Collect – hits GitHub API; writes two raw files:
//     • e2e-raw-jobs.json  – job/step timings + collection metadata
//     • e2e-raw-junit.json – per-run per-spec pass/fail/skip results
//  2. Compute – reads both raw files; aggregates and produces reportData.
//  3. Render  – generates HTML, Slack JSON, and snapshot files.
//
// Step 1 is automatically skipped when e2e-health/e2e-raw-jobs.json already exists.
// Delete it (and e2e-health/e2e-raw-junit.json) to trigger a fresh collect.
// All output files are written to the e2e-health/ directory (gitignored).
// Requires GH_TOKEN (or GITHUB_TOKEN) and GITHUB_REPOSITORY when collecting.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/scripts/pkg/e2etestutils"
	"github.com/google/go-github/v72/github"
	"github.com/spf13/cobra"
)

const (
	defaultWorkflow     = "pr-e2e-testing.yaml"
	defaultBranch       = "main"
	defaultRuns         = 14
	defaultTopN         = 10
	outDir = "e2e-health"

	defaultRawJobs      = outDir + "/e2e-raw-jobs.json"
	defaultRawJunit     = outDir + "/e2e-raw-junit.json"
	defaultDiscovery    = outDir + "/e2e-discovery.json"
	defaultOutputHTML  = outDir + "/e2e-health-report.html"
	defaultSummaryJSON = outDir + "/e2e-health-summary.json"
	defaultReportData  = outDir + "/e2e-health-report-data.json"

	// infraInstabilityThreshold: fraction of specs that must fail in a single
	// run for it to be classified as an infra instability event (excluded from
	// per-spec flake counts).
	infraInstabilityThreshold = 0.30

	// minPhaseDisplaySecs: jobs and steps with avg duration below this are
	// collapsed into an "↳ other" row instead of being shown individually.
	minPhaseDisplaySecs = 20.0
)

// specTiming is a type alias for the canonical SpecTiming from e2etestutils,
// ensuring the JSON format stays in sync with update_test_timings.
type specTiming = e2etestutils.SpecTiming

type runRef struct {
	RunID  int64  `json:"run_id"`
	RunURL string `json:"run_url"`
	Date   string `json:"date"`
}

// collectMeta holds the collection context saved alongside the raw files.
type collectMeta struct {
	Branch       string `json:"branch"`
	Workflow     string `json:"workflow"`
	RepoURL      string `json:"repo_url"`
	GeneratedAt  string `json:"generated_at"`
	AnalyzedRuns int    `json:"analyzed_runs"`
}

// rawJobsFile is the top-level structure of e2e-raw-jobs.json.
type rawJobsFile struct {
	Meta collectMeta   `json:"meta"`
	Runs []rawRunEntry `json:"runs"`
}

// rawStep is the raw per-step data saved during collection.
type rawStep struct {
	Name        string  `json:"name"`
	DurationSec float64 `json:"duration_sec"`
}

// rawJobEntry is the raw per-job data saved during collection.
type rawJobEntry struct {
	Name        string    `json:"name"`
	StartedAt   string    `json:"started_at"`   // RFC3339
	CompletedAt string    `json:"completed_at"` // RFC3339
	Steps       []rawStep `json:"steps"`
}

// rawRunEntry is one workflow run's job/step timing data.
type rawRunEntry struct {
	RunID      int64         `json:"run_id"`
	RunURL     string        `json:"run_url"`
	Date       string        `json:"date"`
	StartedAt  string        `json:"started_at"` // run.GetRunStartedAt(), RFC3339
	Conclusion string        `json:"conclusion"`
	Jobs       []rawJobEntry `json:"jobs"`
}

// rawSpecResult is the outcome of one spec in one shard's JUnit XML output.
// This is the unprocessed data as read from the artifact — no merging applied.
type rawSpecResult struct {
	Passed      bool    `json:"passed"`
	Skipped     bool    `json:"skipped"`
	DurationSec float64 `json:"duration_sec,omitempty"` // wall-clock seconds from JUnit <testcase time="">
	FailureMsg  string  `json:"failure_msg,omitempty"`
	GinkgoLabel string  `json:"ginkgo_label,omitempty"`
	Suite       string  `json:"suite,omitempty"` // Ginkgo suite description (classname in JUnit XML)
}

// rawJUnitFile holds the parsed JUnit results for one shard artifact from one run.
// e2e-raw-junit.json is a flat list of these — one per JUnit XML file downloaded.
// Shard merging, infra instability detection, and per-spec aggregation are all
// deferred to compute.go so that this file is truly unprocessed API output.
//
// Specs includes regular test specs AND one entry per suite whose key is
// "__suite__:SuiteName" carrying the [BeforeSuite] wall-clock duration.
// These special entries drive per-suite overhead in the LPT simulation.
type rawJUnitFile struct {
	RunID  int64                    `json:"run_id"`
	RunURL string                   `json:"run_url"`
	Date   string                   `json:"date"`
	Specs  map[string]rawSpecResult `json:"specs"`
}

func githubToken() string {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GITHUB_TOKEN")
}

func repoFromFlag(flag string) (owner, repo string, err error) {
	parts := strings.SplitN(flag, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("--repo must be in owner/repo format, got %q", flag)
	}
	return parts[0], parts[1], nil
}

func repoFromEnv() (owner, repo string, err error) {
	v := os.Getenv("GITHUB_REPOSITORY")
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY not set or invalid (expected owner/repo)")
	}
	return parts[0], parts[1], nil
}

func buildRepoURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s", owner, repo)
}

// loadOrGenerateDiscovery loads discovery specs from path.
// If the file does not exist, ginkgo is run with --dry-run and --label-filter sanity
// to generate it (matching the filter used by the pr-e2e-testing.yaml CI workflow).
func loadOrGenerateDiscovery(path, _ string) ([]e2etestutils.SpecInfo, error) {
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("Discovery file %s not found — running ginkgo dry-run...\n", path)
		// Resolve the output path to an absolute path before changing Dir,
		// since --json-report is interpreted relative to cmd.Dir.
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve discovery path: %w", err)
		}
		// Resolve repo root via git so the tool works from any working directory.
		rootOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return nil, fmt.Errorf("resolve repo root (git rev-parse): %w", err)
		}
		repoRoot := strings.TrimSpace(string(rootOut))
		// Use "go run" so ginkgo is always the version pinned in go.mod —
		// no external binary needed, no CLI/package version mismatch.
		cmd := exec.Command("go", "run", "github.com/onsi/ginkgo/v2/ginkgo",
			"run", "--dry-run",
			"--label-filter", "sanity",
			"--json-report", absPath,
			"./test/e2e/...",
		)
		cmd.Dir = repoRoot
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("ginkgo dry-run failed: %w", err)
		}
		fmt.Printf("Discovery written to %s\n", path)
	} else {
		fmt.Printf("Found %s — skipping discovery, loading from disk...\n", path)
	}
	return e2etestutils.LoadDiscovery(path)
}

func loadJSON[T any](path string, dest *T) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func newRootCmd() *cobra.Command {
	var (
		repoFlag      string
		workflow      string
		nRuns         int
		topN          int
		outDirFlag    string
		rawJobsPath   string
		rawJunitPath  string
		discoveryPath string
		outputHTML    string
		summaryJSON   string
		reportData    string
	)

	// perFileFlags are the individual path flags that conflict with --out-dir.
	perFileFlags := []string{"raw-jobs", "raw-junit", "discovery", "output", "json-summary", "report-data"}

	cmd := &cobra.Command{
		Use:   "analyze_e2e_health",
		Short: "Generate a unified weekly HTML health report for the e2e CI pipeline",
		Long: `Collects JUnit XML artifacts and raw job/step timing from the GitHub Actions
API, then computes and renders a self-contained HTML report.

All output is written to the e2e-health/ directory (gitignored):
  e2e-health/e2e-raw-jobs.json  – job/step timings + collection metadata
  e2e-health/e2e-raw-junit.json – per-run per-spec pass/fail/skip results
  e2e-health/e2e-health-report.html – self-contained HTML report
  e2e-health/e2e-health-summary.json – Slack notification payload

All aggregation is performed from raw files at report time; changing compute or
render logic never requires re-fetching from the API.

If --raw-jobs already exists on disk the collect step is skipped. Delete it
(and --raw-junit) to trigger a fresh collect.

Requires GH_TOKEN (or GITHUB_TOKEN) and GITHUB_REPOSITORY when collecting.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --out-dir is mutually exclusive with individual path flags.
			if outDirFlag != "" {
				for _, f := range perFileFlags {
					if cmd.Flags().Changed(f) {
						return fmt.Errorf("--out-dir cannot be combined with --%s", f)
					}
				}
			rawJobsPath = filepath.Join(outDirFlag, "e2e-raw-jobs.json")
			rawJunitPath = filepath.Join(outDirFlag, "e2e-raw-junit.json")
			discoveryPath = filepath.Join(outDirFlag, "e2e-discovery.json")
			outputHTML = filepath.Join(outDirFlag, "e2e-health-report.html")
			summaryJSON = filepath.Join(outDirFlag, "e2e-health-summary.json")
			reportData = filepath.Join(outDirFlag, "e2e-health-report-data.json")
			}

			specs, err := loadOrGenerateDiscovery(discoveryPath, "")
			if err != nil {
				return fmt.Errorf("discovery: %w", err)
			}
			fmt.Printf("Loaded discovery: %d specs from %s\n", len(specs), discoveryPath)

			dir := filepath.Dir(rawJobsPath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create output dir %s: %w", dir, err)
			}

			var jobsFile rawJobsFile
			var junitFiles []rawJUnitFile

			if _, err := os.Stat(rawJobsPath); err == nil {
				// Raw files exist — skip the GitHub API fetch.
				fmt.Printf("Found %s — skipping collect, loading from disk...\n", rawJobsPath)
				if err := loadJSON(rawJobsPath, &jobsFile); err != nil {
					return fmt.Errorf("load raw jobs: %w", err)
				}
				if err := loadJSON(rawJunitPath, &junitFiles); err != nil {
					return fmt.Errorf("load raw junit: %w", err)
				}
				fmt.Printf("Loaded: branch=%s, %d job runs, %d junit shard files\n",
					jobsFile.Meta.Branch, len(jobsFile.Runs), len(junitFiles))
			} else {
				token := githubToken()
				if token == "" {
					return fmt.Errorf("GH_TOKEN or GITHUB_TOKEN environment variable is not set")
				}

				var owner, repo string
				var err error
				if repoFlag != "" {
					owner, repo, err = repoFromFlag(repoFlag)
				} else {
					owner, repo, err = repoFromEnv()
				}
				if err != nil {
					return err
				}

				ctx := context.Background()
				client := github.NewClient(nil).WithAuthToken(token)

				fmt.Printf("Collecting health data for %s/%s (last %d runs of %s)...\n", owner, repo, nRuns, workflow)
				var err2 error
				jobsFile, junitFiles, err2 = collectHealthData(ctx, client, owner, repo, workflow, nRuns)
				if err2 != nil {
					return fmt.Errorf("collect: %w", err2)
				}
				if err2 = writeJSON(rawJobsPath, jobsFile); err2 != nil {
					return fmt.Errorf("write raw jobs: %w", err2)
				}
				if err2 = writeJSON(rawJunitPath, junitFiles); err2 != nil {
					return fmt.Errorf("write raw junit: %w", err2)
				}
				fmt.Printf("Collect complete: %d runs, wrote %s and %s\n",
					jobsFile.Meta.AnalyzedRuns, rawJobsPath, rawJunitPath)
			}

			fmt.Println("Computing and rendering HTML report...")
			report := computeReport(jobsFile, junitFiles, specs, topN)

			if err := writeJSON(reportData, report); err != nil {
				return fmt.Errorf("write report data: %w", err)
			}
			fmt.Printf("Wrote %s\n", reportData)

			if err := renderHTML(report, outputHTML); err != nil {
				return fmt.Errorf("render HTML: %w", err)
			}
			fmt.Printf("Wrote %s\n", outputHTML)

			if err := renderSlackSummary(report, summaryJSON); err != nil {
				return fmt.Errorf("render slack summary: %w", err)
			}
			fmt.Printf("Wrote %s\n", summaryJSON)

			if err := appendStepSummary(report); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write step summary: %v\n", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFlag, "repo", "", "GitHub repository owner/repo (default: $GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&workflow, "workflow", defaultWorkflow, "Workflow filename to analyze")
	cmd.Flags().IntVar(&nRuns, "runs", defaultRuns, "Number of recent completed runs to analyze")
	cmd.Flags().IntVar(&topN, "top", defaultTopN, "Entries to show per ranking table")
	cmd.Flags().StringVar(&outDirFlag, "out-dir", "", "Directory for all output files (mutually exclusive with individual path flags; default: "+outDir+")")
	cmd.Flags().StringVar(&rawJobsPath, "raw-jobs", defaultRawJobs, "Path to e2e-raw-jobs.json (conflicts with --out-dir)")
	cmd.Flags().StringVar(&rawJunitPath, "raw-junit", defaultRawJunit, "Path to e2e-raw-junit.json (conflicts with --out-dir)")
	cmd.Flags().StringVar(&discoveryPath, "discovery", defaultDiscovery, "Path to Ginkgo dry-run discovery JSON (conflicts with --out-dir)")
	cmd.Flags().StringVar(&outputHTML, "output", defaultOutputHTML, "Path for HTML report output (conflicts with --out-dir)")
	cmd.Flags().StringVar(&summaryJSON, "json-summary", defaultSummaryJSON, "Path for Slack JSON summary (conflicts with --out-dir)")
	cmd.Flags().StringVar(&reportData, "report-data", defaultReportData, "Path for computed report data JSON (conflicts with --out-dir)")

	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

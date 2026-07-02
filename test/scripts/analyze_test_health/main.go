// Command analyze_test_health produces a unified weekly HTML health report for
// a CI test workflow (unit or integration), covering flaky tests, slowest tests,
// and a per-run pass-rate trend.
//
// Data flow:
//
//  1. Collect – hits GitHub API; writes one raw file:
//     • <out-dir>/raw-junit.json – per-run per-spec pass/fail/skip results
//  2. Compute – reads the raw file; aggregates and produces reportData.
//  3. Render  – generates HTML, Slack JSON, and step summary.
//
// Step 1 is automatically skipped when <out-dir>/raw-junit.json already exists.
// Delete it to trigger a fresh collect.
// All output files are written to <out-dir>/ (gitignored).
// Requires GH_TOKEN (or GITHUB_TOKEN) and GITHUB_REPOSITORY when collecting.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
	"github.com/spf13/cobra"
)

const (
	defaultBranch = "main"
	defaultRuns   = 14
	defaultTopN   = 10

	// infraInstabilityThreshold: fraction of specs that must fail in a single
	// run for it to be classified as an infra instability event (excluded from
	// per-spec flake counts).
	infraInstabilityThreshold = 0.30
)

// collectMeta holds the collection context saved alongside the raw file.
type collectMeta struct {
	Branch       string `json:"branch"`
	Workflow     string `json:"workflow"`
	ArtifactName string `json:"artifact_name"`
	Title        string `json:"title"`
	RepoURL      string `json:"repo_url"`
	GeneratedAt  string `json:"generated_at"`
	AnalyzedRuns int    `json:"analyzed_runs"`
}

// rawFile is the top-level structure of raw-junit.json.
type rawFile struct {
	Meta collectMeta  `json:"meta"`
	Runs []rawRunData `json:"runs"`
}

// rawRunData is one workflow run's parsed JUnit data.
// Specs is nil when the run produced no JUnit artifact (tests skipped or
// run failed before tests could run). CollectError is set when the artifact
// download or parse failed; these runs are excluded from all aggregation.
type rawRunData struct {
	RunID        int64                    `json:"run_id"`
	RunURL       string                   `json:"run_url"`
	Date         string                   `json:"date"`
	Conclusion   string                   `json:"conclusion"`
	CollectError string                   `json:"collect_error,omitempty"`
	Specs        map[string]rawSpecResult `json:"specs,omitempty"`
}

// rawSpecResult is the outcome of one test case in a run's JUnit XML output.
type rawSpecResult struct {
	Passed      bool    `json:"passed"`
	Skipped     bool    `json:"skipped"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	FailureMsg  string  `json:"failure_msg,omitempty"`
}

// runRef is a lightweight run reference used in reportData.
type runRef struct {
	RunID  int64  `json:"run_id"`
	RunURL string `json:"run_url"`
	Date   string `json:"date"`
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
		repoFlag     string
		workflow     string
		artifactName string
		title        string
		nRuns        int
		topN         int
		outDirFlag   string
		rawJunitPath string
		outputHTML   string
		summaryJSON  string
		reportData   string
	)

	perFileFlags := []string{"raw-junit", "output", "json-summary", "report-data"}

	cmd := &cobra.Command{
		Use:   "analyze_test_health",
		Short: "Generate a unified weekly HTML health report for a CI test workflow",
		Long: `Collects JUnit XML artifacts from GitHub Actions and renders a self-contained
HTML report covering flaky tests, slowest tests, and a per-run pass-rate trend.

All output is written to the <out-dir>/ directory (gitignored):
  <out-dir>/raw-junit.json        – per-run per-spec pass/fail/skip results
  <out-dir>/<prefix>-report.html  – self-contained HTML report
  <out-dir>/<prefix>-summary.json – Slack notification payload

All aggregation is performed from the raw file at report time; changing
compute or render logic never requires re-fetching from the API.

If raw-junit.json already exists on disk the collect step is skipped. Delete
it to trigger a fresh collect.

Requires GH_TOKEN (or GITHUB_TOKEN) and GITHUB_REPOSITORY when collecting.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if workflow == "" {
				return fmt.Errorf("--workflow is required")
			}
			if artifactName == "" {
				return fmt.Errorf("--artifact-name is required")
			}

			// --out-dir is mutually exclusive with individual path flags.
			if outDirFlag != "" {
				for _, f := range perFileFlags {
					if cmd.Flags().Changed(f) {
						return fmt.Errorf("--out-dir cannot be combined with --%s", f)
					}
				}
				prefix := strings.TrimSuffix(filepath.Base(outDirFlag), "/")
				rawJunitPath = filepath.Join(outDirFlag, "raw-junit.json")
				outputHTML = filepath.Join(outDirFlag, prefix+"-report.html")
				summaryJSON = filepath.Join(outDirFlag, prefix+"-summary.json")
				reportData = filepath.Join(outDirFlag, prefix+"-report-data.json")
			}

			for _, p := range []string{rawJunitPath, outputHTML, summaryJSON, reportData} {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return fmt.Errorf("create output dir for %s: %w", p, err)
				}
			}

			var raw rawFile

			if _, err := os.Stat(rawJunitPath); err == nil {
				// Raw file exists — skip the GitHub API fetch.
				fmt.Printf("Found %s — skipping collect, loading from disk...\n", rawJunitPath)
				if err := loadJSON(rawJunitPath, &raw); err != nil {
					return fmt.Errorf("load raw junit: %w", err)
				}
				fmt.Printf("Loaded: workflow=%s, %d runs\n", raw.Meta.Workflow, len(raw.Runs))
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

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			client := github.NewClient(nil).WithAuthToken(token)

			fmt.Printf("Collecting health data for %s/%s (last %d runs of %s)...\n", owner, repo, nRuns, workflow)
			var err2 error
			raw, err2 = collectHealthData(ctx, client, owner, repo, workflow, artifactName, title, nRuns)
				if err2 != nil {
					return fmt.Errorf("collect: %w", err2)
				}
				if err2 = writeJSON(rawJunitPath, raw); err2 != nil {
					return fmt.Errorf("write raw junit: %w", err2)
				}
				fmt.Printf("Collect complete: %d runs, wrote %s\n", raw.Meta.AnalyzedRuns, rawJunitPath)
			}

			// If title was not set via flag but we loaded from disk, use meta title.
			if title == "" {
				title = raw.Meta.Title
			}
			if title == "" {
				title = "Test Health Report"
			}

			fmt.Println("Computing and rendering HTML report...")
			report := computeReport(raw, title, topN)

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

	defaultOutDir := "test-health"

	cmd.Flags().StringVar(&repoFlag, "repo", "", "GitHub repository owner/repo (default: $GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow filename to analyze (required)")
	cmd.Flags().StringVar(&artifactName, "artifact-name", "", "Exact artifact name containing JUnit XML (required)")
	cmd.Flags().StringVar(&title, "title", "Test Health Report", "Report title")
	cmd.Flags().IntVar(&nRuns, "runs", defaultRuns, "Number of recent completed runs to analyze")
	cmd.Flags().IntVar(&topN, "top", defaultTopN, "Entries to show per ranking table")
	cmd.Flags().StringVar(&outDirFlag, "out-dir", "", "Directory for all output files (mutually exclusive with individual path flags; default: "+defaultOutDir+")")
	cmd.Flags().StringVar(&rawJunitPath, "raw-junit", defaultOutDir+"/raw-junit.json", "Path to raw-junit.json (conflicts with --out-dir)")
	cmd.Flags().StringVar(&outputHTML, "output", defaultOutDir+"/test-health-report.html", "Path for HTML report output (conflicts with --out-dir)")
	cmd.Flags().StringVar(&summaryJSON, "json-summary", defaultOutDir+"/test-health-summary.json", "Path for Slack JSON summary (conflicts with --out-dir)")
	cmd.Flags().StringVar(&reportData, "report-data", defaultOutDir+"/test-health-report-data.json", "Path for computed report data JSON (conflicts with --out-dir)")

	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

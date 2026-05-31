// Command update_test_timings refreshes the committed test-timings.json cache
// by mining per-spec average durations from JUnit XML report artifacts of
// the last N successful e2e CI runs.
//
// The cache maps each spec's full name (as it appears in the JUnit <testcase>
// name attribute) to its average wall-clock duration in seconds.  Skipped
// specs and specs with zero or negative durations are excluded.
//
// Run automatically by .github/workflows/update-test-timings.yaml.
// Requires GITHUB_TOKEN (or GH_TOKEN) in the environment.
// GITHUB_REPOSITORY must be set (e.g. "flightctl/flightctl") or passed via
// --repo.
//
// Usage:
//
//	go run ./test/scripts/update_test_timings \
//	    [--runs N] \
//	    [--workflow pr-e2e-testing.yaml] \
//	    [--repo owner/repo] \
//	    [--output test/scripts/test-timings.json]
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-github/v72/github"
	"github.com/spf13/cobra"
)

// junitTestSuites is the root element of Ginkgo's JUnit XML output.
type junitTestSuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	TestSuites []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	TestCases []junitTestCase `xml:"testcase"`
}

// junitTestCase holds the fields we need from each <testcase> element.
// Skipped is non-nil when a <skipped/> child element is present.
type junitTestCase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Time      float64    `xml:"time,attr"`
	Skipped   []struct{} `xml:"skipped"`
}

// suiteOverheadPrefix is the key prefix used to store per-suite BeforeSuite
// average durations in the timings cache, e.g. "__suite__:Agent E2E Suite".
const suiteOverheadPrefix = "__suite__:"

func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

func repoFromEnv() (owner, repo string, err error) {
	v := os.Getenv("GITHUB_REPOSITORY") // "owner/repo"
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY not set or invalid (expected owner/repo)")
	}
	return parts[0], parts[1], nil
}

func listSuccessfulRunIDs(ctx context.Context, client *github.Client, owner, repo, workflow string, limit int) ([]int64, error) {
	opts := &github.ListWorkflowRunsOptions{
		Branch: "main",
		Status: "success",
		ListOptions: github.ListOptions{
			PerPage: limit,
		},
	}
	runs, _, err := client.Actions.ListWorkflowRunsByFileName(ctx, owner, repo, workflow, opts)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	ids := make([]int64, 0, len(runs.WorkflowRuns))
	for _, r := range runs.WorkflowRuns {
		ids = append(ids, r.GetID())
	}
	return ids, nil
}

// listMatchingArtifacts returns all artifacts whose name starts with prefix.
func listMatchingArtifacts(ctx context.Context, client *github.Client, owner, repo string, runID int64, prefix string) ([]*github.Artifact, error) {
	var matched []*github.Artifact
	opts := &github.ListOptions{PerPage: 100}
	for {
		artifacts, resp, err := client.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, opts)
		if err != nil {
			return nil, fmt.Errorf("list artifacts for run %d: %w", runID, err)
		}
		for _, a := range artifacts.Artifacts {
			if strings.HasPrefix(a.GetName(), prefix) {
				matched = append(matched, a)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return matched, nil
}

// downloadAndExtractArtifact downloads an artifact ZIP and extracts all *.xml
// files into destDir, prefixing each filename with the artifact ID to avoid
// collisions when multiple artifacts from the same run share the same filename.
func downloadAndExtractArtifact(ctx context.Context, client *github.Client, owner, repo string, artifactID int64, destDir string) error {
	// DownloadArtifact follows redirects and returns the final URL.
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
		return fmt.Errorf("download artifact: %w", err)
	}
	defer resp.Body.Close()

	// Write ZIP to a temp file so we can seek through it.
	tmp, err := os.CreateTemp("", "artifact-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("write artifact zip: %w", err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fmt.Errorf("open artifact zip: %w", err)
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

// junitSpecName extracts the plain spec name from a Ginkgo JUnit testcase name.
//
// Ginkgo formats testcase names as:
//
//	[It] Container1 Container2 LeafText [label1, label2, ...]
//
// We strip the "[It] " prefix and the trailing " [labels]" group so that the
// resulting key matches what compute_test_assignments produces from the
// discovery JSON (ContainerHierarchyTexts joined with LeafNodeText).
// Non-It entries (e.g. [BeforeSuite]) return an empty string and are skipped.
func junitSpecName(name string) string {
	const itPrefix = "[It] "
	if !strings.HasPrefix(name, itPrefix) {
		return ""
	}
	name = strings.TrimPrefix(name, itPrefix)
	// Strip trailing " [label1, label2, ...]" — the last bracket group.
	if idx := strings.LastIndex(name, " ["); idx > 0 && strings.HasSuffix(name, "]") {
		name = name[:idx]
	}
	return name
}

func parseTimingsFromFile(path string) (map[string][]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var suites junitTestSuites
	if err := xml.Unmarshal(data, &suites); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	result := make(map[string][]float64)
	for _, suite := range suites.TestSuites {
		for _, tc := range suite.TestCases {
			if len(tc.Skipped) > 0 || tc.Time <= 0 {
				continue
			}
			if tc.Name == "[BeforeSuite]" && tc.ClassName != "" {
				key := suiteOverheadPrefix + tc.ClassName
				result[key] = append(result[key], tc.Time)
				continue
			}
			key := junitSpecName(tc.Name)
			if key == "" {
				continue
			}
			result[key] = append(result[key], tc.Time)
		}
	}
	return result, nil
}

func loadExistingCache(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]float64{}, nil
		}
		return nil, fmt.Errorf("read existing cache: %w", err)
	}
	var cache map[string]float64
	if err := json.Unmarshal(data, &cache); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not parse existing cache %s; starting fresh: %v\n", path, err)
		return map[string]float64{}, nil
	}
	return cache, nil
}

func writeCache(path string, timings map[string]float64) error {
	keys := make([]string, 0, len(timings))
	for k := range timings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make(map[string]float64, len(timings))
	for _, k := range keys {
		ordered[k] = math.Round(timings[k]*1000) / 1000
	}

	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal timings: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func printSummary(timings map[string]float64, prevCount int) {
	if len(timings) == 0 {
		fmt.Println("No timing data collected.")
		return
	}
	vals := make([]float64, 0, len(timings))
	for _, v := range timings {
		vals = append(vals, v)
	}
	sort.Float64s(vals)
	var sum float64
	for _, v := range vals {
		sum += v
	}
	newCount := len(timings) - prevCount
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("Timing cache updated")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("  Total specs tracked : %d\n", len(timings))
	if newCount > 0 {
		fmt.Printf("  New specs added     : %d\n", newCount)
	}
	fmt.Printf("  Min duration        : %.1fs\n", vals[0])
	fmt.Printf("  Max duration        : %.1fs\n", vals[len(vals)-1])
	fmt.Printf("  Mean duration       : %.1fs\n", sum/float64(len(vals)))
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()
}

func newRootCmd() *cobra.Command {
	var (
		nRuns    int
		workflow string
		repoFlag string
		output   string
	)

	cmd := &cobra.Command{
		Use:   "update_test_timings",
		Short: "Refresh the e2e test-timings.json cache from GitHub Actions artifacts",
		Long: `Fetches JUnit XML report artifacts (junit-results-*) from the last N
successful runs of the e2e CI workflow, computes per-spec average durations,
and writes the result to the committed test-timings.json cache.

Requires GITHUB_TOKEN (or GH_TOKEN) in the environment.
GITHUB_REPOSITORY must be set (e.g. "flightctl/flightctl") or passed via --repo.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := githubToken()
			if token == "" {
				return fmt.Errorf("GITHUB_TOKEN (or GH_TOKEN) environment variable is not set")
			}

			var owner, repo string
			if repoFlag != "" {
				parts := strings.SplitN(repoFlag, "/", 2)
				if len(parts) != 2 {
					return fmt.Errorf("--repo must be in owner/repo format")
				}
				owner, repo = parts[0], parts[1]
			} else {
				var err error
				owner, repo, err = repoFromEnv()
				if err != nil {
					return err
				}
			}

			ctx := context.Background()
			client := github.NewClient(nil).WithAuthToken(token)

			existingCache, err := loadExistingCache(output)
			if err != nil {
				return err
			}
			prevCount := len(existingCache)

			fmt.Printf("Fetching last %d successful runs of %q in %s/%s...\n", nRuns, workflow, owner, repo)
			runIDs, err := listSuccessfulRunIDs(ctx, client, owner, repo, workflow, nRuns)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error listing workflow runs: %v\nLeaving existing cache unchanged.\n", err)
				return nil
			}
			if len(runIDs) == 0 {
				fmt.Println("No successful runs found. Leaving existing cache unchanged.")
				return nil
			}
			fmt.Printf("Found %d run(s)\n", len(runIDs))

			allObs := make(map[string][]float64)

			for _, runID := range runIDs {
				fmt.Printf("  Processing run %d...\n", runID)

			artifacts, err := listMatchingArtifacts(ctx, client, owner, repo, runID, "junit-results-")
			if err != nil {
				fmt.Fprintf(os.Stderr, "    Warning: %v\n", err)
				continue
			}
			if len(artifacts) == 0 {
				fmt.Printf("    No junit-results artifacts found\n")
				continue
			}

				tmpDir, err := os.MkdirTemp("", fmt.Sprintf("e2e-timings-%d-*", runID))
				if err != nil {
					return err
				}

				for _, a := range artifacts {
					if err := downloadAndExtractArtifact(ctx, client, owner, repo, a.GetID(), tmpDir); err != nil {
						fmt.Fprintf(os.Stderr, "    Warning: download %s: %v\n", a.GetName(), err)
						continue
					}
				}

			entries, _ := os.ReadDir(tmpDir)
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
					continue
				}
					obs, err := parseTimingsFromFile(filepath.Join(tmpDir, e.Name()))
					if err != nil {
						fmt.Fprintf(os.Stderr, "    Warning: parse %s: %v\n", e.Name(), err)
						continue
					}
					for name, durations := range obs {
						allObs[name] = append(allObs[name], durations...)
					}
				}
				os.RemoveAll(tmpDir)
			}

			if len(allObs) == 0 {
				fmt.Println("No timing data found in downloaded artifacts. Leaving existing cache unchanged.")
				return nil
			}

			newTimings := make(map[string]float64, len(allObs))
			for name, durations := range allObs {
				var sum float64
				for _, d := range durations {
					sum += d
				}
				newTimings[name] = sum / float64(len(durations))
			}
			fmt.Printf("Computed averages for %d spec(s).\n", len(newTimings))

			if err := writeCache(output, newTimings); err != nil {
				return err
			}
			printSummary(newTimings, prevCount)
			return nil
		},
	}

	cmd.Flags().IntVar(&nRuns, "runs", 10, "Number of recent successful runs to aggregate")
	cmd.Flags().StringVar(&workflow, "workflow", "pr-e2e-testing.yaml", "Workflow filename to query")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "GitHub repository slug owner/repo (default: $GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&output, "output", "test/scripts/test-timings.json", "Path to write updated timings")

	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

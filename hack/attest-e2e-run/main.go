// Command attest-e2e-run verifies a completed PR E2E run before issuing reuse proof.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	sourceWorkflowPath      = ".github/workflows/pr-e2e-testing.yaml"
	candidateArtifactPrefix = "e2e-candidate-tree-v2-"
	proofVersion            = 2
	maxPullRequestFiles     = 3000
	maxAPIResponseBytes     = 20 << 20
	maxArtifactArchiveBytes = 20 << 20
	maxCandidateRecordBytes = 1 << 20
)

type config struct {
	repository           string
	serverURL            string
	runID                string
	sourceRunID          string
	sourceRunAttempt     string
	sourceEvent          string
	sourceConclusion     string
	sourceHeadSHA        string
	sourceHeadBranch     string
	sourceHeadRepository string
	outputPath           string
	summaryPath          string
}

type githubClient struct {
	apiBase *url.URL
	token   string
	client  *http.Client
}

type repository struct {
	FullName string "json:\"full_name\""
}

type workflowRun struct {
	ID             int64      "json:\"id\""
	RunAttempt     int        "json:\"run_attempt\""
	Event          string     "json:\"event\""
	Status         string     "json:\"status\""
	Conclusion     string     "json:\"conclusion\""
	Repository     repository "json:\"repository\""
	Path           string     "json:\"path\""
	HeadSHA        string     "json:\"head_sha\""
	HeadBranch     string     "json:\"head_branch\""
	HeadRepository repository "json:\"head_repository\""
}

type branchRef struct {
	Ref  string      "json:\"ref\""
	SHA  string      "json:\"sha\""
	Repo *repository "json:\"repo\""
}

type pullRequestLabel struct {
	Name string "json:\"name\""
}

type pullRequest struct {
	Number         int                "json:\"number\""
	State          string             "json:\"state\""
	Base           branchRef          "json:\"base\""
	Head           branchRef          "json:\"head\""
	Labels         []pullRequestLabel "json:\"labels\""
	MergeCommitSHA string             "json:\"merge_commit_sha\""
	ChangedFiles   *int               "json:\"changed_files\""
}

type pullRequestFile struct {
	Filename         string "json:\"filename\""
	PreviousFilename string "json:\"previous_filename\""
}

type workflowJob struct {
	Name       string "json:\"name\""
	Conclusion string "json:\"conclusion\""
}

type workflowArtifact struct {
	ID      int64  "json:\"id\""
	Name    string "json:\"name\""
	Expired bool   "json:\"expired\""
}

type candidate struct {
	Version          int    "json:\"version\""
	Tree             string "json:\"tree\""
	TestedSHA        string "json:\"tested_sha\""
	BaseSHA          string "json:\"base_sha\""
	HeadSHA          string "json:\"head_sha\""
	SourceRunID      string "json:\"source_run_id\""
	SourceRunAttempt string "json:\"source_run_attempt\""
}

type candidateExpectation struct {
	ArtifactName     string
	TestedSHA        string
	BaseSHA          string
	HeadSHA          string
	SourceRunID      string
	SourceRunAttempt string
}

type proof struct {
	Version          int    "json:\"version\""
	Tree             string "json:\"tree\""
	TestedSHA        string "json:\"tested_sha\""
	BaseSHA          string "json:\"base_sha\""
	HeadSHA          string "json:\"head_sha\""
	SourceRunID      string "json:\"source_run_id\""
	SourceRunAttempt string "json:\"source_run_attempt\""
	SourcePRNumber   int    "json:\"source_pr_number\""
	E2EResult        string "json:\"e2e_result\""
	APIResult        string "json:\"api_result\""
	AttestorRunID    string "json:\"attestor_run_id\""
}

type commitResponse struct {
	Commit struct {
		Tree struct {
			SHA string "json:\"sha\""
		} "json:\"tree\""
	} "json:\"commit\""
	Parents []struct {
		SHA string "json:\"sha\""
	} "json:\"parents\""
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fatal(err)
	}

	client, err := newGitHubClient(os.Getenv("GITHUB_API_URL"), os.Getenv("GH_TOKEN"))
	if err != nil {
		fatal(err)
	}

	result, reason, err := attest(context.Background(), cfg, client)
	if err != nil {
		reason = "the source E2E run could not be verified: " + err.Error()
	}
	if reason != "" {
		if err := publishRejection(cfg, reason); err != nil {
			fatal(err)
		}
		return
	}
	if err := publishProof(cfg, result); err != nil {
		fatal(err)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		repository:           os.Getenv("GITHUB_REPOSITORY"),
		serverURL:            os.Getenv("GITHUB_SERVER_URL"),
		runID:                os.Getenv("GITHUB_RUN_ID"),
		sourceRunID:          os.Getenv("SOURCE_RUN_ID"),
		sourceRunAttempt:     os.Getenv("SOURCE_RUN_ATTEMPT"),
		sourceEvent:          os.Getenv("SOURCE_EVENT"),
		sourceConclusion:     os.Getenv("SOURCE_CONCLUSION"),
		sourceHeadSHA:        os.Getenv("SOURCE_HEAD_SHA"),
		sourceHeadBranch:     os.Getenv("SOURCE_HEAD_BRANCH"),
		sourceHeadRepository: os.Getenv("SOURCE_HEAD_REPOSITORY"),
		outputPath:           os.Getenv("GITHUB_OUTPUT"),
		summaryPath:          os.Getenv("GITHUB_STEP_SUMMARY"),
	}
	for name, value := range map[string]string{
		"GITHUB_REPOSITORY":   cfg.repository,
		"GITHUB_RUN_ID":       cfg.runID,
		"SOURCE_RUN_ID":       cfg.sourceRunID,
		"SOURCE_RUN_ATTEMPT":  cfg.sourceRunAttempt,
		"GITHUB_OUTPUT":       cfg.outputPath,
		"GITHUB_STEP_SUMMARY": cfg.summaryPath,
	} {
		if strings.TrimSpace(value) == "" {
			return config{}, fmt.Errorf("required environment variable %s is missing", name)
		}
	}
	if cfg.serverURL == "" {
		cfg.serverURL = "https://github.com"
	}
	if _, _, ok := splitRepository(cfg.repository); !ok {
		return config{}, fmt.Errorf("GITHUB_REPOSITORY must have owner/repository form")
	}
	if _, err := strconv.ParseInt(cfg.runID, 10, 64); err != nil {
		return config{}, fmt.Errorf("GITHUB_RUN_ID must be numeric")
	}
	if _, err := strconv.ParseInt(cfg.sourceRunID, 10, 64); err != nil {
		return config{}, fmt.Errorf("SOURCE_RUN_ID must be numeric")
	}
	if _, err := strconv.Atoi(cfg.sourceRunAttempt); err != nil {
		return config{}, fmt.Errorf("SOURCE_RUN_ATTEMPT must be numeric")
	}
	return cfg, nil
}

func newGitHubClient(apiURL, token string) (*githubClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("GH_TOKEN is missing")
	}
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	base, err := url.Parse(apiURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("GITHUB_API_URL is invalid")
	}
	return &githubClient{
		apiBase: base,
		token:   token,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("GitHub API response exceeded the redirect limit")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing a non-HTTPS GitHub API redirect")
				}
				return nil
			},
		},
	}, nil
}

func (c *githubClient) apiURL(apiPath string, query url.Values) string {
	u := *c.apiBase
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(apiPath, "/")
	u.RawPath = ""
	if query != nil {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (c *githubClient) request(ctx context.Context, targetURL string, maxBytes int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, resp.Header.Clone(), fmt.Errorf("GitHub API returned %s for %s", resp.Status, req.URL.Path)
	}
	if resp.ContentLength > maxBytes {
		return nil, resp.Header.Clone(), fmt.Errorf("GitHub API response exceeded %d bytes", maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, resp.Header.Clone(), err
	}
	if int64(len(body)) > maxBytes {
		return nil, resp.Header.Clone(), fmt.Errorf("GitHub API response exceeded %d bytes", maxBytes)
	}
	return body, resp.Header.Clone(), nil
}

func (c *githubClient) getJSON(ctx context.Context, apiPath string, dst any) error {
	body, _, err := c.request(ctx, c.apiURL(apiPath, nil), maxAPIResponseBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func listPages[T any](ctx context.Context, client *githubClient, firstURL string, parse func([]byte) ([]T, error)) ([]T, error) {
	var all []T
	pageURL := firstURL
	for pageURL != "" {
		body, headers, err := client.request(ctx, pageURL, maxAPIResponseBytes)
		if err != nil {
			return nil, err
		}
		items, err := parse(body)
		if err != nil {
			return nil, fmt.Errorf("decode paginated GitHub API response: %w", err)
		}
		all = append(all, items...)
		pageURL, err = client.nextPageURL(headers.Get("Link"))
		if err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (c *githubClient) nextPageURL(linkHeader string) (string, error) {
	for _, link := range strings.Split(linkHeader, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 || !strings.Contains(parts[1], "rel=\"next\"") && !strings.Contains(parts[1], "rel=next") {
			continue
		}
		linkURL := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(linkURL, "<") || !strings.HasSuffix(linkURL, ">") {
			return "", fmt.Errorf("GitHub API returned an invalid pagination link")
		}
		linkURL = strings.TrimSuffix(strings.TrimPrefix(linkURL, "<"), ">")
		u, err := url.Parse(linkURL)
		if err != nil {
			return "", fmt.Errorf("GitHub API returned an invalid pagination URL")
		}
		if !u.IsAbs() {
			u = c.apiBase.ResolveReference(u)
		}
		if u.Scheme != c.apiBase.Scheme || u.Host != c.apiBase.Host || u.User != nil {
			return "", fmt.Errorf("GitHub API returned a pagination link on an unexpected host")
		}
		return u.String(), nil
	}
	return "", nil
}

func attest(ctx context.Context, cfg config, client *githubClient) (proof, string, error) {
	if cfg.sourceEvent != "pull_request" || cfg.sourceConclusion != "success" {
		return proof{}, "the source run was not a successful pull request workflow", nil
	}

	runID, _ := strconv.ParseInt(cfg.sourceRunID, 10, 64)
	runAttempt, _ := strconv.Atoi(cfg.sourceRunAttempt)
	repoPath := strings.TrimPrefix(cfg.repository, "/")
	var sourceRun workflowRun
	if err := client.getJSON(ctx, fmt.Sprintf("repos/%s/actions/runs/%d", repoPath, runID), &sourceRun); err != nil {
		return proof{}, "", err
	}
	sourcePathOK := sourceRun.Path == sourceWorkflowPath || strings.HasPrefix(sourceRun.Path, sourceWorkflowPath+"@")
	if sourceRun.Event != "pull_request" ||
		sourceRun.Status != "completed" ||
		sourceRun.Conclusion != "success" ||
		sourceRun.Repository.FullName != cfg.repository ||
		!sourcePathOK ||
		sourceRun.ID != runID ||
		sourceRun.RunAttempt != runAttempt ||
		sourceRun.HeadSHA != cfg.sourceHeadSHA ||
		sourceRun.HeadBranch != cfg.sourceHeadBranch ||
		sourceRun.HeadRepository.FullName != cfg.sourceHeadRepository {
		return proof{}, "the source workflow metadata did not match the completed run", nil
	}

	headOwner, _, ok := splitRepository(cfg.sourceHeadRepository)
	if !ok {
		return proof{}, "the source repository could not be identified", nil
	}
	var repoDetails struct {
		DefaultBranch string "json:\"default_branch\""
	}
	if err := client.getJSON(ctx, "repos/"+repoPath, &repoDetails); err != nil {
		return proof{}, "", err
	}

	pullQuery := url.Values{
		"state":    {"open"},
		"head":     {headOwner + ":" + cfg.sourceHeadBranch},
		"per_page": {"100"},
	}
	pullRequests, err := listPages(ctx, client, client.apiURL("repos/"+repoPath+"/pulls", pullQuery), parseJSONArray[pullRequest])
	if err != nil {
		return proof{}, "", err
	}
	matches := matchingPullRequests(pullRequests, cfg, repoDetails.DefaultBranch)
	if len(matches) != 1 {
		return proof{}, "there was not exactly one open, labeled PR for the source run", nil
	}

	var currentPR pullRequest
	if err := client.getJSON(ctx, fmt.Sprintf("repos/%s/pulls/%d", repoPath, matches[0].Number), &currentPR); err != nil {
		return proof{}, "", err
	}
	if !matchesPR(currentPR, cfg, repoDetails.DefaultBranch) {
		return proof{}, "the source pull request changed while it was being verified", nil
	}
	if currentPR.ChangedFiles == nil || *currentPR.ChangedFiles < 0 || *currentPR.ChangedFiles > maxPullRequestFiles {
		return proof{}, "the source pull request file diff exceeds the verifiable GitHub API limit", nil
	}

	files, err := listPages(ctx, client, client.apiURL(
		fmt.Sprintf("repos/%s/pulls/%d/files", repoPath, currentPR.Number),
		url.Values{"per_page": {"100"}},
	), parseJSONArray[pullRequestFile])
	if err != nil {
		return proof{}, "", err
	}
	if len(files) != *currentPR.ChangedFiles {
		return proof{}, "the source pull request file list was incomplete", nil
	}
	if containsTrustedWorkflowChange(files) {
		return proof{}, "the source pull request changed trusted workflow or action files", nil
	}

	jobsQuery := url.Values{
		"filter":   {"latest"},
		"per_page": {"100"},
	}
	jobs, err := listPages(ctx, client, client.apiURL(
		fmt.Sprintf("repos/%s/actions/runs/%d/jobs", repoPath, runID),
		jobsQuery,
	), parseJobsPage)
	if err != nil {
		return proof{}, "", err
	}
	if err := validateTestJobs(jobs); err != nil {
		return proof{}, err.Error(), nil
	}

	artifacts, err := listPages(ctx, client, client.apiURL(
		fmt.Sprintf("repos/%s/actions/runs/%d/artifacts", repoPath, runID),
		url.Values{"per_page": {"100"}},
	), parseArtifactsPage)
	if err != nil {
		return proof{}, "", err
	}
	candidateArtifacts := make([]workflowArtifact, 0, 1)
	for _, artifact := range artifacts {
		if !artifact.Expired && strings.HasPrefix(artifact.Name, candidateArtifactPrefix) {
			candidateArtifacts = append(candidateArtifacts, artifact)
		}
	}
	if len(candidateArtifacts) != 1 {
		return proof{}, "the source run did not contain exactly one proof candidate", nil
	}

	candidateData, err := client.downloadArtifactRecord(ctx, repoPath, candidateArtifacts[0].ID)
	if err != nil {
		return proof{}, "", err
	}
	var record candidate
	if err := json.Unmarshal(candidateData, &record); err != nil {
		return proof{}, "", fmt.Errorf("decode E2E proof candidate: %w", err)
	}
	if err := validateCandidate(record, candidateExpectation{
		ArtifactName:     candidateArtifacts[0].Name,
		TestedSHA:        currentPR.MergeCommitSHA,
		BaseSHA:          currentPR.Base.SHA,
		HeadSHA:          currentPR.Head.SHA,
		SourceRunID:      cfg.sourceRunID,
		SourceRunAttempt: cfg.sourceRunAttempt,
	}); err != nil {
		return proof{}, err.Error(), nil
	}

	if !isObjectID(currentPR.MergeCommitSHA) {
		return proof{}, "GitHub did not provide a current PR merge commit", nil
	}
	var testedCommit commitResponse
	if err := client.getJSON(ctx, fmt.Sprintf("repos/%s/commits/%s", repoPath, currentPR.MergeCommitSHA), &testedCommit); err != nil {
		return proof{}, "", err
	}
	if testedCommit.Commit.Tree.SHA != record.Tree ||
		!hasParent(testedCommit.Parents, currentPR.Base.SHA) ||
		!hasParent(testedCommit.Parents, currentPR.Head.SHA) {
		return proof{}, "the candidate tree did not match the current PR merge commit", nil
	}

	return proof{
		Version:          proofVersion,
		Tree:             record.Tree,
		TestedSHA:        currentPR.MergeCommitSHA,
		BaseSHA:          currentPR.Base.SHA,
		HeadSHA:          currentPR.Head.SHA,
		SourceRunID:      cfg.sourceRunID,
		SourceRunAttempt: cfg.sourceRunAttempt,
		SourcePRNumber:   currentPR.Number,
		E2EResult:        "success",
		APIResult:        "success",
		AttestorRunID:    cfg.runID,
	}, "", nil
}

func matchingPullRequests(pullRequests []pullRequest, cfg config, defaultBranch string) []pullRequest {
	var matches []pullRequest
	for _, pullRequest := range pullRequests {
		if matchesPR(pullRequest, cfg, defaultBranch) {
			matches = append(matches, pullRequest)
		}
	}
	return matches
}

func matchesPR(p pullRequest, cfg config, defaultBranch string) bool {
	return p.State == "open" &&
		p.Base.Repo != nil &&
		p.Base.Repo.FullName == cfg.repository &&
		p.Base.Ref == defaultBranch &&
		p.Head.Repo != nil &&
		p.Head.Repo.FullName == cfg.sourceHeadRepository &&
		p.Head.Ref == cfg.sourceHeadBranch &&
		p.Head.SHA == cfg.sourceHeadSHA &&
		hasRunE2ELabel(p.Labels)
}

func hasRunE2ELabel(labels []pullRequestLabel) bool {
	for _, label := range labels {
		if label.Name == "run-e2e" {
			return true
		}
	}
	return false
}

func containsTrustedWorkflowChange(files []pullRequestFile) bool {
	for _, file := range files {
		if isGitHubPath(file.Filename) || isGitHubPath(file.PreviousFilename) {
			return true
		}
	}
	return false
}

func isGitHubPath(file string) bool {
	return file == ".github" || strings.HasPrefix(file, ".github/")
}

func validateTestJobs(jobs []workflowJob) error {
	counts := map[string]int{"e2e-tests": 0, "api-tests": 0}
	for _, job := range jobs {
		target := ""
		switch {
		case isWorkflowJobName(job.Name, "e2e-tests"):
			target = "e2e-tests"
		case isWorkflowJobName(job.Name, "api-tests"):
			target = "api-tests"
		default:
			continue
		}
		counts[target]++
		if job.Conclusion != "success" {
			return fmt.Errorf("the source %s job did not pass", target)
		}
	}
	if counts["e2e-tests"] == 0 || counts["api-tests"] == 0 {
		return fmt.Errorf("the source E2E matrix and API jobs did not both pass")
	}
	return nil
}

func isWorkflowJobName(name, jobID string) bool {
	return name == jobID ||
		strings.HasPrefix(name, jobID+" (") ||
		strings.HasPrefix(name, jobID+" / ")
}

func validateCandidate(record candidate, expected candidateExpectation) error {
	if record.Version != proofVersion ||
		!isObjectID(record.Tree) ||
		record.TestedSHA != expected.TestedSHA ||
		record.BaseSHA != expected.BaseSHA ||
		record.HeadSHA != expected.HeadSHA ||
		record.SourceRunID != expected.SourceRunID ||
		record.SourceRunAttempt != expected.SourceRunAttempt ||
		expected.ArtifactName != candidateArtifactPrefix+record.Tree {
		return fmt.Errorf("the proof candidate did not match the tested commit and PR metadata")
	}
	return nil
}

func hasParent(parents []struct {
	SHA string "json:\"sha\""
}, sha string) bool {
	for _, parent := range parents {
		if parent.SHA == sha {
			return true
		}
	}
	return false
}

func isObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func splitRepository(fullName string) (string, string, bool) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseJSONArray[T any](body []byte) ([]T, error) {
	var items []T
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func parseJobsPage(body []byte) ([]workflowJob, error) {
	var page struct {
		Jobs []workflowJob "json:\"jobs\""
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Jobs, nil
}

func parseArtifactsPage(body []byte) ([]workflowArtifact, error) {
	var page struct {
		Artifacts []workflowArtifact "json:\"artifacts\""
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Artifacts, nil
}

func (c *githubClient) downloadArtifactRecord(ctx context.Context, repoPath string, artifactID int64) ([]byte, error) {
	archive, _, err := c.request(ctx, c.apiURL(
		fmt.Sprintf("repos/%s/actions/artifacts/%d/zip", repoPath, artifactID),
		nil,
	), maxArtifactArchiveBytes)
	if err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open E2E proof candidate archive: %w", err)
	}
	var record []byte
	for _, file := range reader.File {
		if path.Base(file.Name) != "e2e-candidate.json" {
			continue
		}
		if record != nil {
			return nil, fmt.Errorf("proof candidate archive contained multiple candidate records")
		}
		if file.UncompressedSize64 > maxCandidateRecordBytes {
			return nil, fmt.Errorf("proof candidate record exceeded %d bytes", maxCandidateRecordBytes)
		}
		opened, err := file.Open()
		if err != nil {
			return nil, err
		}
		record, err = io.ReadAll(io.LimitReader(opened, maxCandidateRecordBytes+1))
		_ = opened.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(record)) > maxCandidateRecordBytes {
			return nil, fmt.Errorf("proof candidate record exceeded %d bytes", maxCandidateRecordBytes)
		}
	}
	if record == nil {
		return nil, fmt.Errorf("proof candidate archive did not contain e2e-candidate.json")
	}
	return record, nil
}

func publishRejection(cfg config, reason string) error {
	escapedReason := escapeWorkflowCommandData(reason)
	fmt.Printf("::notice::No reusable E2E proof: %s\n", escapedReason)
	if err := appendFile(cfg.outputPath, "attested=false\n"); err != nil {
		return err
	}
	return appendFile(cfg.summaryPath, "## E2E proof attestation\n\nNo proof issued: "+reason+".\n")
}

func publishProof(cfg config, result proof) error {
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.WriteFile("e2e-proof.json", content, 0o600); err != nil {
		return fmt.Errorf("write trusted E2E proof: %w", err)
	}
	output := fmt.Sprintf(
		"attested=true\ntree=%s\nsource_run=%s\nsource_pr=%d\n",
		result.Tree,
		result.SourceRunID,
		result.SourcePRNumber,
	)
	if err := appendFile(cfg.outputPath, output); err != nil {
		return err
	}
	summary := fmt.Sprintf(
		"## E2E proof attestation\n\nIssued trusted proof for PR [#%d](%s/%s/pull/%d), tree \u0060%s\u0060.\nVerified source E2E and API jobs in [run #%s](%s/%s/actions/runs/%s).\n",
		result.SourcePRNumber,
		strings.TrimSuffix(cfg.serverURL, "/"),
		cfg.repository,
		result.SourcePRNumber,
		result.Tree,
		result.SourceRunID,
		strings.TrimSuffix(cfg.serverURL, "/"),
		cfg.repository,
		result.SourceRunID,
	)
	return appendFile(cfg.summaryPath, summary)
}

func appendFile(filename, content string) error {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	return nil
}

func escapeWorkflowCommandData(value string) string {
	replacer := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	return replacer.Replace(value)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

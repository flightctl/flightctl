package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAttest(t *testing.T) {
	tests := []struct {
		name           string
		changedFile    string
		wantAttested   bool
		wantReasonPart string
	}{
		{
			name:         "When verified E2E and API jobs pass, it should issue proof",
			changedFile:  "internal/example.go",
			wantAttested: true,
		},
		{
			name:           "When the PR changes a workflow, it should refuse proof",
			changedFile:    ".github/workflows/unsafe.yaml",
			wantReasonPart: "changed trusted workflow or action files",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, client := newMockAttestor(t, test.changedFile)
			got, reason, err := attest(context.Background(), cfg, client)
			if err != nil {
				t.Fatalf("attest() error = %v", err)
			}
			if test.wantAttested {
				if reason != "" {
					t.Fatalf("attest() rejected a valid run: %s", reason)
				}
				if got.SourcePRNumber != 42 || got.Tree != strings.Repeat("a", 40) {
					t.Fatalf("attest() proof = %#v, want PR 42 and the tested tree", got)
				}
				return
			}
			if reason == "" || !strings.Contains(reason, test.wantReasonPart) {
				t.Fatalf("attest() reason = %q, want it to contain %q", reason, test.wantReasonPart)
			}
		})
	}
}

func newMockAttestor(t *testing.T, changedFile string) (config, *githubClient) {
	t.Helper()

	const (
		repo          = "flightctl/flightctl"
		sourceRepo    = "asaf/flightctl"
		sourceRunID   = "1234"
		sourceAttempt = "2"
		sourcePR      = 42
		attestorRunID = "5678"
		sourceBranch  = "feature/e2e-proof"
		defaultBranch = "main"
	)
	baseSHA := strings.Repeat("b", 40)
	headSHA := strings.Repeat("c", 40)
	testedSHA := strings.Repeat("d", 40)
	treeSHA := strings.Repeat("a", 40)
	changedFileCount := 1

	sourceRun := workflowRun{
		ID:             1234,
		RunAttempt:     2,
		Event:          "pull_request",
		Status:         "completed",
		Conclusion:     "success",
		Repository:     repository{FullName: repo},
		Path:           sourceWorkflowPath + "@refs/pull/42/merge",
		HeadSHA:        headSHA,
		HeadBranch:     sourceBranch,
		HeadRepository: repository{FullName: sourceRepo},
	}
	pr := pullRequest{
		Number: sourcePR,
		State:  "open",
		Base: branchRef{
			Ref:  defaultBranch,
			SHA:  baseSHA,
			Repo: &repository{FullName: repo},
		},
		Head: branchRef{
			Ref:  sourceBranch,
			SHA:  headSHA,
			Repo: &repository{FullName: sourceRepo},
		},
		Labels:         []pullRequestLabel{{Name: "run-e2e"}},
		MergeCommitSHA: testedSHA,
		ChangedFiles:   &changedFileCount,
	}
	candidateJSON, err := json.Marshal(candidate{
		Version:          proofVersion,
		Tree:             treeSHA,
		TestedSHA:        testedSHA,
		BaseSHA:          baseSHA,
		HeadSHA:          headSHA,
		SourceRunID:      sourceRunID,
		SourceRunAttempt: sourceAttempt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifact bytes.Buffer
	zipWriter := zip.NewWriter(&artifact)
	candidateFile, err := zipWriter.Create("e2e-candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := candidateFile.Write(candidateJSON); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		switch r.URL.Path {
		case "/repos/" + repo + "/actions/runs/" + sourceRunID:
			writeJSONResponse(t, w, sourceRun)
		case "/repos/" + repo:
			writeJSONResponse(t, w, map[string]string{"default_branch": defaultBranch})
		case "/repos/" + repo + "/pulls":
			if r.URL.Query().Get("head") != "asaf:"+sourceBranch {
				t.Errorf("pull request head query = %q", r.URL.Query().Get("head"))
			}
			writeJSONResponse(t, w, []pullRequest{pr})
		case fmt.Sprintf("/repos/%s/pulls/%d", repo, sourcePR):
			writeJSONResponse(t, w, pr)
		case fmt.Sprintf("/repos/%s/pulls/%d/files", repo, sourcePR):
			writeJSONResponse(t, w, []pullRequestFile{{Filename: changedFile}})
		case "/repos/" + repo + "/actions/runs/" + sourceRunID + "/jobs":
			writeJSONResponse(t, w, map[string]any{
				"jobs": []workflowJob{
					{Name: "e2e-tests (cs9-bootc, helm)", Conclusion: "success"},
					{Name: "e2e-tests (cs10-bootc, helm)", Conclusion: "success"},
					{Name: "api-tests / api-test", Conclusion: "success"},
				},
			})
		case "/repos/" + repo + "/actions/runs/" + sourceRunID + "/artifacts":
			writeJSONResponse(t, w, map[string]any{
				"artifacts": []workflowArtifact{{
					ID:      901,
					Name:    candidateArtifactPrefix + treeSHA,
					Expired: false,
				}},
			})
		case fmt.Sprintf("/repos/%s/actions/artifacts/%d/zip", repo, 901):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(artifact.Bytes())
		case "/repos/" + repo + "/commits/" + testedSHA:
			writeJSONResponse(t, w, map[string]any{
				"commit": map[string]any{
					"tree": map[string]string{"sha": treeSHA},
				},
				"parents": []map[string]string{
					{"sha": baseSHA},
					{"sha": headSHA},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	apiBase, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	return config{
			repository:           repo,
			serverURL:            "https://github.com",
			runID:                attestorRunID,
			sourceRunID:          sourceRunID,
			sourceRunAttempt:     sourceAttempt,
			sourceEvent:          "pull_request",
			sourceConclusion:     "success",
			sourceHeadSHA:        headSHA,
			sourceHeadBranch:     sourceBranch,
			sourceHeadRepository: sourceRepo,
		}, &githubClient{
			apiBase: apiBase,
			token:   "test-token",
			client:  server.Client(),
		}
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode mock GitHub response: %v", err)
	}
}

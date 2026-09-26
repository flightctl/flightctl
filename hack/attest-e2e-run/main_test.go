package main

import (
	"strings"
	"testing"
)

func TestContainsTrustedWorkflowChange(t *testing.T) {
	tests := []struct {
		name  string
		files []pullRequestFile
		want  bool
	}{
		{
			name:  "When a workflow file changes, it should reject",
			files: []pullRequestFile{{Filename: ".github/workflows/test.yaml"}},
			want:  true,
		},
		{
			name: "When a workflow file is renamed out of the trusted directory, it should reject",
			files: []pullRequestFile{{
				Filename:         "hack/attestor.go",
				PreviousFilename: ".github/scripts/attestor.sh",
			}},
			want: true,
		},
		{
			name:  "When a similarly named unrelated path changes, it should allow",
			files: []pullRequestFile{{Filename: ".github-workflows/test.yaml"}},
			want:  false,
		},
		{
			name:  "When a regular source file changes, it should allow",
			files: []pullRequestFile{{Filename: "internal/example.go"}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsTrustedWorkflowChange(test.files); got != test.want {
				t.Fatalf("containsTrustedWorkflowChange() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestValidateCandidate(t *testing.T) {
	tree := strings.Repeat("a", 40)
	expected := candidateExpectation{
		ArtifactName:     candidateArtifactPrefix + tree,
		TestedSHA:        strings.Repeat("b", 40),
		BaseSHA:          strings.Repeat("c", 40),
		HeadSHA:          strings.Repeat("d", 40),
		SourceRunID:      "1234",
		SourceRunAttempt: "2",
	}
	record := candidate{
		Version:          proofVersion,
		Tree:             tree,
		TestedSHA:        expected.TestedSHA,
		BaseSHA:          expected.BaseSHA,
		HeadSHA:          expected.HeadSHA,
		SourceRunID:      expected.SourceRunID,
		SourceRunAttempt: expected.SourceRunAttempt,
	}

	if err := validateCandidate(record, expected); err != nil {
		t.Fatalf("valid candidate was rejected: %v", err)
	}

	record.SourceRunAttempt = "1"
	if err := validateCandidate(record, expected); err == nil {
		t.Fatal("candidate from a different source run attempt was accepted")
	}
}

func TestValidateTestJobs(t *testing.T) {
	tests := []struct {
		name string
		jobs []workflowJob
		want bool
	}{
		{
			name: "When both E2E matrix jobs and the delegated API job pass, it should issue proof",
			jobs: []workflowJob{
				{Name: "e2e-tests (cs9-bootc, helm)", Conclusion: "success"},
				{Name: "e2e-tests (cs10-bootc, helm)", Conclusion: "success"},
				{Name: "api-tests / api-test", Conclusion: "success"},
			},
			want: true,
		},
		{
			name: "When one E2E matrix job fails, it should reject",
			jobs: []workflowJob{
				{Name: "e2e-tests (cs9-bootc, helm)", Conclusion: "success"},
				{Name: "e2e-tests (cs10-bootc, helm)", Conclusion: "failure"},
				{Name: "api-tests", Conclusion: "success"},
			},
			want: false,
		},
		{
			name: "When the API job is skipped, it should reject",
			jobs: []workflowJob{
				{Name: "e2e-tests (cs9-bootc, helm)", Conclusion: "success"},
				{Name: "e2e-tests (cs10-bootc, helm)", Conclusion: "success"},
				{Name: "api-tests", Conclusion: "skipped"},
			},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTestJobs(test.jobs)
			if (err == nil) != test.want {
				t.Fatalf("validateTestJobs() error = %v, want success %t", err, test.want)
			}
		})
	}
}

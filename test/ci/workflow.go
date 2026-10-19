// Package ci holds regression tests for the parts of the GitHub Actions
// workflows that decide which end-to-end suites run and which images get
// built for them. That decision is made in YAML and shell rather than in Go,
// so it is otherwise untested: a renamed output or a dropped "with:" entry
// silently turns an opt-in suite into a suite that is never selected, or into
// one selected without the image it needs.
//
// The helpers here read the real workflow files from the repository, so the
// tests fail when the workflows change rather than when a copy of them does.
package ci

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Workflow paths, relative to this package's directory.
const (
	PRE2ETestingWorkflow     = "pr-e2e-testing.yaml"
	RunE2ETestsWorkflow      = "run-e2e-tests.yaml"
	BuildImagesChartWorkflow = "build-images-and-charts.yaml"
)

// Step is one step of a workflow job. Only the fields these tests assert on
// are modelled; everything else in the file is ignored.
type Step struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

// Job is one workflow job, whether it runs steps or calls another workflow.
type Job struct {
	Uses    string            `yaml:"uses"`
	If      string            `yaml:"if"`
	Needs   yaml.Node         `yaml:"needs"`
	With    map[string]any    `yaml:"with"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []Step            `yaml:"steps"`
}

// Input is one workflow_call input declaration.
type Input struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Type        string `yaml:"type"`
	Default     any    `yaml:"default"`
}

// Workflow is the subset of a workflow file these tests read.
type Workflow struct {
	Name string `yaml:"name"`
	On   struct {
		WorkflowCall struct {
			Inputs map[string]Input `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]Job `yaml:"jobs"`
}

// LoadWorkflow reads and parses a workflow from .github/workflows.
func LoadWorkflow(name string) (*Workflow, error) {
	path := filepath.Join("..", "..", ".github", "workflows", name)
	raw, err := os.ReadFile(path) //nolint:gosec // G304: fixed in-repo workflow path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var wf Workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &wf, nil
}

// Job returns a named job.
func (w *Workflow) Job(name string) (Job, bool) {
	job, ok := w.Jobs[name]
	return job, ok
}

// Step returns the first step of a job whose id, or failing that whose name,
// matches. Steps are identified by id where one exists because a name is
// prose and gets reworded.
func (j Job) Step(idOrName string) (Step, bool) {
	for _, step := range j.Steps {
		if step.ID == idOrName {
			return step, true
		}
	}
	for _, step := range j.Steps {
		if step.Name == idOrName {
			return step, true
		}
	}
	return Step{}, false
}

// With returns a "with:" value rendered as the string the workflow file
// holds, so an expression is compared as written.
func (j Job) WithValue(key string) (string, bool) {
	value, ok := j.With[key]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%v", value), true
}

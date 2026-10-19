package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/ci"
	"github.com/stretchr/testify/require"
)

// The catalog collector suite is opt-in. One flag resolved from the PR's
// run-e2e labels has to reach two places: the build, so the collector image
// is produced, and the e2e run, so the suite is told which image to use and
// that its absence is fatal. These tests exercise the real selection script
// and the real wiring between the workflows, because nothing else does.

const (
	selectJob  = "select-e2e"
	selectStep = "selection"

	collectorSelectedOutput = "collector_selected"
	buildCollectorInput     = "build_catalog_collector"
	runCollectorInput       = "catalog_collector_selected"
)

// collectorEnvNames are the three settings the collector suite reads. They
// must be exported for a selected run and absent otherwise.
var collectorEnvNames = []string{
	"E2E_CATALOG_COLLECTOR_IMAGE",
	"E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED",
	"E2E_CATALOG_COLLECTOR_REQUIRED",
}

// runSelection executes the real "Resolve E2E labels" script with the given
// event and PR labels, and returns the step outputs it wrote.
func runSelection(t *testing.T, eventName, labelsJSON string) (map[string]string, error) {
	t.Helper()

	wf, err := ci.LoadWorkflow(ci.PRE2ETestingWorkflow)
	require.NoError(t, err)
	job, ok := wf.Job(selectJob)
	require.Truef(t, ok, "job %q is missing from %s", selectJob, ci.PRE2ETestingWorkflow)
	step, ok := job.Step(selectStep)
	require.Truef(t, ok, "step %q is missing from job %q", selectStep, selectJob)
	require.NotEmpty(t, step.Run, "the selection step has no script")

	outputPath := filepath.Join(t.TempDir(), "github_output")
	require.NoError(t, os.WriteFile(outputPath, nil, 0o600))

	// The script is read from a workflow file in this repository, which is
	// the thing under test; running a copy would prove nothing.
	cmd := exec.Command("bash", "-c", step.Run) //nolint:gosec // G204: in-repo workflow script, read from a fixed path
	cmd.Env = append(os.Environ(),
		"EVENT_NAME="+eventName,
		"PR_LABELS_JSON="+labelsJSON,
		"GITHUB_OUTPUT="+outputPath,
	)
	combined, runErr := cmd.CombinedOutput()

	raw, readErr := os.ReadFile(outputPath) //nolint:gosec // G304: path created by this test
	require.NoError(t, readErr)

	outputs := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		require.Truef(t, found, "unparseable step output line %q", line)
		outputs[key] = value
	}
	if runErr != nil {
		return outputs, &scriptError{err: runErr, output: string(combined)}
	}
	return outputs, nil
}

type scriptError struct {
	err    error
	output string
}

func (e *scriptError) Error() string { return e.err.Error() + ": " + e.output }

func requireTooling(t *testing.T) {
	t.Helper()
	// The selection script is plain bash and jq, the same tooling the e2e
	// scripts and GitHub runners already rely on.
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; the selection script cannot be exercised", tool)
		}
	}
}

func TestSelectE2EResolvesCollectorSelection(t *testing.T) {
	requireTooling(t)

	const cs10Base = "sanity && agent && !microshift"

	tests := []struct {
		name              string
		eventName         string
		labels            string
		wantEnabled       string
		wantSanity        string
		wantCollector     string
		wantLabelFilter   string
		wantCS10Filter    string
		wantScriptFailure bool
	}{
		{
			name:            "When the pull request carries no run-e2e label, it should disable e2e and not select the collector",
			eventName:       "pull_request",
			labels:          `["release-1.4","risk-ask"]`,
			wantEnabled:     "false",
			wantSanity:      "false",
			wantCollector:   "false",
			wantLabelFilter: "",
			wantCS10Filter:  cs10Base,
		},
		{
			name:            "When the pull request carries run-e2e, it should select sanity only and not the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e"]`,
			wantEnabled:     "true",
			wantSanity:      "true",
			wantCollector:   "false",
			wantLabelFilter: "sanity",
			wantCS10Filter:  cs10Base,
		},
		{
			name:            "When the event is not a pull request, it should select sanity only and not the collector",
			eventName:       "push",
			labels:          `[]`,
			wantEnabled:     "true",
			wantSanity:      "true",
			wantCollector:   "false",
			wantLabelFilter: "sanity",
			wantCS10Filter:  cs10Base,
		},
		{
			name:            "When the whole collector suite is requested, it should select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector",
			wantCS10Filter:  cs10Base + " || catalogcollector && !microshift",
		},
		{
			name:            "When the collector is combined with sanity in one label, it should select both and the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:sanity+catalogcollector"]`,
			wantEnabled:     "true",
			wantSanity:      "true",
			wantCollector:   "true",
			wantLabelFilter: "sanity || catalogcollector",
			wantCS10Filter:  cs10Base + " || catalogcollector && !microshift",
		},
		{
			name:            "When run-e2e and a collector label are separate labels, it should select both and the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e","run-e2e:catalogcollector"]`,
			wantEnabled:     "true",
			wantSanity:      "true",
			wantCollector:   "true",
			wantLabelFilter: "sanity || catalogcollector",
			wantCS10Filter:  cs10Base + " || catalogcollector && !microshift",
		},
		{
			name:            "When only the smoke scenario is requested, it should select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector-smoke"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector-smoke",
			wantCS10Filter:  cs10Base + " || catalogcollector-smoke && !microshift",
		},
		{
			name:            "When only the update scenario is requested, it should select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector-update"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector-update",
			wantCS10Filter:  cs10Base + " || catalogcollector-update && !microshift",
		},
		{
			name:            "When only the removal scenario is requested, it should select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector-removal"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector-removal",
			wantCS10Filter:  cs10Base + " || catalogcollector-removal && !microshift",
		},
		{
			name:            "When only the failure-safety scenario is requested, it should select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector-failure-safety"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector-failure-safety",
			wantCS10Filter:  cs10Base + " || catalogcollector-failure-safety && !microshift",
		},
		{
			name:            "When all four scenarios are requested together, it should select the collector once",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalogcollector-smoke+catalogcollector-update+catalogcollector-removal+catalogcollector-failure-safety"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "true",
			wantLabelFilter: "catalogcollector-smoke || catalogcollector-update || catalogcollector-removal || catalogcollector-failure-safety",
			wantCS10Filter: cs10Base +
				" || catalogcollector-smoke && !microshift" +
				" || catalogcollector-update && !microshift" +
				" || catalogcollector-removal && !microshift" +
				" || catalogcollector-failure-safety && !microshift",
		},
		{
			name:            "When another opt-in suite is requested, it should not select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:rollout"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "false",
			wantLabelFilter: "rollout",
			wantCS10Filter:  cs10Base + " || rollout && !microshift",
		},
		{
			name:            "When a label merely starts like the collector's, it should not select the collector",
			eventName:       "pull_request",
			labels:          `["run-e2e:catalog"]`,
			wantEnabled:     "true",
			wantSanity:      "false",
			wantCollector:   "false",
			wantLabelFilter: "catalog",
			wantCS10Filter:  cs10Base + " || catalog && !microshift",
		},
		{
			name:              "When the run-e2e label suffix is empty, it should fail",
			eventName:         "pull_request",
			labels:            `["run-e2e:"]`,
			wantScriptFailure: true,
		},
		{
			name:              "When the run-e2e label suffix has an empty element, it should fail",
			eventName:         "pull_request",
			labels:            `["run-e2e:sanity+"]`,
			wantScriptFailure: true,
		},
		{
			name:              "When the run-e2e label suffix is not a Ginkgo label, it should fail",
			eventName:         "pull_request",
			labels:            `["run-e2e:catalog collector"]`,
			wantScriptFailure: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputs, err := runSelection(t, test.eventName, test.labels)
			if test.wantScriptFailure {
				require.Error(t, err, "the selection script should reject this label")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantEnabled, outputs["enabled"], "enabled")
			require.Equal(t, test.wantSanity, outputs["includes_sanity"], "includes_sanity")
			require.Equal(t, test.wantCollector, outputs[collectorSelectedOutput], collectorSelectedOutput)
			require.Equal(t, test.wantLabelFilter, outputs["label_filter"], "label_filter")
			require.Equal(t, test.wantCS10Filter, outputs["cs10_label_filter"], "cs10_label_filter")
		})
	}
}

func TestSelectE2EPublishesCollectorSelection(t *testing.T) {
	wf, err := ci.LoadWorkflow(ci.PRE2ETestingWorkflow)
	require.NoError(t, err)
	job, ok := wf.Job(selectJob)
	require.True(t, ok)
	require.Contains(t, job.Outputs, collectorSelectedOutput,
		"select-e2e must publish the collector flag for the downstream jobs")
	require.Equal(t,
		"${{ steps."+selectStep+".outputs."+collectorSelectedOutput+" }}",
		job.Outputs[collectorSelectedOutput])
}

func TestCollectorSelectionReachesBuildAndRun(t *testing.T) {
	wf, err := ci.LoadWorkflow(ci.PRE2ETestingWorkflow)
	require.NoError(t, err)

	wantExpression := "${{ needs." + selectJob + ".outputs." + collectorSelectedOutput + " == 'true' }}"

	tests := []struct {
		name     string
		job      string
		callee   string
		inputKey string
	}{
		{
			name:     "When the build job is called, it should be told whether to build the collector image",
			job:      "build-images-and-charts",
			callee:   "./.github/workflows/" + ci.BuildImagesChartWorkflow,
			inputKey: buildCollectorInput,
		},
		{
			name:     "When the e2e job is called, it should be told whether the collector suite is selected",
			job:      "e2e-tests",
			callee:   "./.github/workflows/" + ci.RunE2ETestsWorkflow,
			inputKey: runCollectorInput,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job, ok := wf.Job(test.job)
			require.Truef(t, ok, "job %q is missing", test.job)
			require.Equal(t, test.callee, job.Uses)
			value, ok := job.WithValue(test.inputKey)
			require.Truef(t, ok, "job %q does not pass %q", test.job, test.inputKey)
			require.Equal(t, wantExpression, value,
				"both callees must be driven by the same selection flag")
		})
	}
}

func TestRunE2EExportsCollectorEnvironmentOnlyWhenSelected(t *testing.T) {
	wf, err := ci.LoadWorkflow(ci.RunE2ETestsWorkflow)
	require.NoError(t, err)

	input, ok := wf.On.WorkflowCall.Inputs[runCollectorInput]
	require.Truef(t, ok, "%s must declare the %q input", ci.RunE2ETestsWorkflow, runCollectorInput)
	require.Equal(t, "boolean", input.Type)
	require.False(t, input.Required)
	require.Equal(t, false, input.Default,
		"an unset caller must get an ordinary run with no collector environment")

	job, ok := wf.Job("e2e-test")
	require.True(t, ok)

	// Exactly one step may mention the collector settings, and it must be
	// gated on the input. A second mention would mean an ordinary sanity run
	// still names a collector image.
	var guarded []string
	for _, step := range job.Steps {
		body := step.Run
		for name, value := range step.Env {
			body += "\n" + name + "=" + value
		}
		mentions := false
		for _, name := range collectorEnvNames {
			if strings.Contains(body, name) {
				mentions = true
				break
			}
		}
		if !mentions {
			continue
		}
		guarded = append(guarded, step.Name)
		require.Containsf(t, step.If, "inputs."+runCollectorInput,
			"step %q sets the collector environment but is not gated on the selection flag", step.Name)
		for _, name := range collectorEnvNames {
			require.Containsf(t, step.Run, name+"=",
				"step %q must export %s", step.Name, name)
		}
		require.Contains(t, step.Run, "GITHUB_ENV",
			"the collector settings must be exported rather than scoped to this step")
	}
	require.Len(t, guarded, 1,
		"the collector environment must come from exactly one gated step, found: %v", guarded)
}

func TestRunE2EHasNoCollectorSpecificReportCheck(t *testing.T) {
	// A step used to read the JUnit report and assert that one named spec was
	// not skipped. It was removed because it has to be edited whenever a spec
	// is renamed, and this test keeps it from coming back. Nothing in the
	// workflow replaces what it checked: requiring the collector only fails
	// an unavailable deployment, which is a different question from whether
	// a spec was selected and executed. The standard JUnit generation and
	// upload steps are unaffected and must stay.
	wf, err := ci.LoadWorkflow(ci.RunE2ETestsWorkflow)
	require.NoError(t, err)
	job, ok := wf.Job("e2e-test")
	require.True(t, ok)

	var uploadsReport bool
	for _, step := range job.Steps {
		require.NotContains(t, step.Run, "imports a LIVE model",
			"step %q hardcodes a catalog collector spec name", step.Name)
		if strings.Contains(step.Uses, "upload-artifact") &&
			strings.Contains(strings.ToLower(step.Name), "test report") {
			uploadsReport = true
		}
	}
	require.True(t, uploadsReport, "the standard JUnit report upload must still exist")
}

func TestBuildImagesBuildsCollectorOnlyWhenAsked(t *testing.T) {
	wf, err := ci.LoadWorkflow(ci.BuildImagesChartWorkflow)
	require.NoError(t, err)

	input, ok := wf.On.WorkflowCall.Inputs[buildCollectorInput]
	require.Truef(t, ok, "%s must declare the %q input", ci.BuildImagesChartWorkflow, buildCollectorInput)
	require.Equal(t, "boolean", input.Type)
	require.False(t, input.Required)
	require.Equal(t, false, input.Default,
		"existing callers must keep getting a build without the collector image")

	job, ok := wf.Job("build-backend-containers")
	require.True(t, ok)

	build, ok := job.Step("Build backend containers")
	require.True(t, ok, "the backend build step was renamed; this test anchors the collector build to it")
	require.Contains(t, build.Run, "flightctl-catalog-collector-container",
		"the collector image must be built by the backend build step")

	// The flag reaches the script through the step env, the way every other
	// caller-controlled value in these workflows does, and the script branches
	// on it rather than building unconditionally.
	var flagVar string
	for name, value := range build.Env {
		if strings.Contains(value, "inputs."+buildCollectorInput) {
			flagVar = name
			break
		}
	}
	require.NotEmptyf(t, flagVar, "the backend build step must receive inputs.%s", buildCollectorInput)
	require.Contains(t, build.Run, flagVar,
		"the collector build must be conditional on the selection flag")

	// The collector must be stamped with the same metadata as every other
	// backend image, which only holds if it is built in this step's env.
	for _, name := range []string{"SOURCE_GIT_TAG", "SOURCE_GIT_TREE_STATE", "SOURCE_GIT_COMMIT", "REGISTRY", "REGISTRY_OWNER", "OS"} {
		require.Containsf(t, build.Env, name,
			"the backend build step must set %s for the collector build to inherit it", name)
	}
	require.Equal(t, "el9", build.Env["OS"])

	// And it must happen before the bundle is taken, or the image never
	// reaches the cluster the suite runs against.
	buildIndex, bundleIndex := -1, -1
	for i, step := range job.Steps {
		switch step.Name {
		case "Build backend containers":
			buildIndex = i
		case "Bundle images":
			bundleIndex = i
		}
	}
	require.NotEqual(t, -1, buildIndex)
	require.NotEqual(t, -1, bundleIndex)
	require.Less(t, buildIndex, bundleIndex,
		"the collector image must exist before the bundle is created")
}

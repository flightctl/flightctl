package k8s

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// These cover the one thing neither a unit test of splitImageRef nor a values
// grep can establish: what the shipped chart actually puts in the collector
// container's image field when it is handed the overrides this package
// generates.
//
// The two halves are written independently. splitImageRef splits a reference
// at its final colon into image.image and image.tag; the chart rejoins them
// with printf "%s:%s" (templates/_helpers.tpl, "flightctl-catalog-collector.image").
// Nothing makes the two agree except that they happen to be inverses, and a
// digest-pinned reference is the case where that is least obvious: the split
// falls inside "@sha256:<digest>", so image.image ends in "@sha256" and the
// tag is the bare digest. The rejoin reproduces the original reference exactly,
// which is why this package does not reject or rewrite digest references.
//
// Rendering is done with the real chart and the real helm binary, because a
// hand-written fmt.Sprintf in the test would only assert that the test agrees
// with itself.

// helmBinary returns the helm to render with, or skips.
//
// helm is part of the standard e2e environment (see the suite README), so this
// runs wherever the e2e suite can run. It is skipped rather than failed
// elsewhere so that "go test ./test/e2e/..." on a machine without helm does
// not turn an unrelated change red - the same gate test/packaging applies to
// podman.
func helmBinary(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv("HELM")); override != "" {
		return override
	}
	path, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not on PATH; set HELM to run the collector chart render tests")
	}
	return path
}

// renderedCollectorImage renders the shipped chart with the supplied image
// overrides and returns the image the collector container actually gets.
func renderedCollectorImage(t *testing.T, repository, tag string) string {
	t.Helper()

	root, err := projectRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	chartDir := filepath.Join(root, collectorChartPath)

	// Bounded the same way every other helm invocation in this package is, so
	// a render that hangs fails the test instead of the whole run. Rendering
	// is local and contacts no cluster, so the bound can be short.
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()

	//nolint:gosec // G204: chart path is a repository constant, overrides come from the table below
	cmd := exec.CommandContext(ctx, helmBinary(t), "template", "collector-render-test", chartDir,
		"--namespace", "flightctl-test",
		// Pinned so the result does not drift with the local helm build's
		// default, matching the chart's own render tests.
		"--kube-version", "1.32.0",
		"--values", filepath.Join(chartDir, "lint-values.yaml"),
		// Exactly the overrides Start passes to installChart.
		"--set-string", "image.image="+repository,
		"--set-string", "image.tag="+tag,
		"--set-string", "image.pullPolicy=IfNotPresent",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("helm template did not finish within %s: %v\n%s", renderTimeout, err, out)
		}
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return collectorContainerImage(t, string(out))
}

// collectorContainerImage digs the collector container's image out of the
// rendered manifests. The whole point of these tests is to read the field the
// cluster would act on, so the YAML is decoded rather than grepped.
func collectorContainerImage(t *testing.T, manifests string) string {
	t.Helper()

	type container struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	var object struct {
		Kind string `json:"kind"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []container `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}

	for index, document := range strings.Split(manifests, "\n---") {
		// Helm emits a "# Source: ..." marker ahead of each document and a
		// trailing empty one. Both are legal YAML and decode fine; only a
		// genuinely empty document is skipped without being read.
		if strings.TrimSpace(document) == "" {
			continue
		}
		object.Kind = ""
		object.Spec.Template.Spec.Containers = nil
		if err := yaml.Unmarshal([]byte(document), &object); err != nil {
			// Not skipped: output this test cannot parse means the render is
			// not what it claims to be, and silently dropping the document
			// could turn a broken Deployment into "container not found" or,
			// worse, into a pass against some other document.
			t.Fatalf("decoding rendered document %d failed: %v\ndocument:\n%s", index, err, document)
		}
		if object.Kind != "Deployment" {
			continue
		}
		for _, c := range object.Spec.Template.Spec.Containers {
			if c.Name == collectorContainerName {
				return c.Image
			}
		}
	}
	t.Fatalf("no %q container found in the rendered manifests:\n%s", collectorContainerName, manifests)
	return ""
}

const (
	// collectorContainerName is the container the chart names in its Deployment.
	collectorContainerName = "catalog-collector"
	// renderTimeout bounds one "helm template" call. Rendering is local and
	// contacts no cluster, so this only has to be generous enough for a cold
	// start of the binary.
	renderTimeout = 2 * time.Minute
)

func TestCollectorImageOverridesSurviveTheChart(t *testing.T) {
	// A syntactically valid digest: "sha256:" and 64 lowercase hex characters.
	// Nothing is pulled - these tests only render - so it does not have to
	// correspond to a real image.
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		// reference is what resolveImage would have produced.
		reference string
		// wantRepository and wantTag are the overrides splitImageRef must
		// derive, i.e. exactly what Start passes to the chart.
		wantRepository string
		wantTag        string
		// wantImage is the image the rendered Deployment must carry. For a
		// reference that already names a tag or a digest this is the input
		// unchanged; for one that names neither it is the input normalized
		// with the ":latest" splitImageRef defaults to.
		wantImage string
	}{
		{
			name:           "When the reference is digest-pinned it should render the same reference back",
			reference:      "quay.io/flightctl/flightctl-catalog-collector-el9@" + digest,
			wantRepository: "quay.io/flightctl/flightctl-catalog-collector-el9@sha256",
			wantTag:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantImage:      "quay.io/flightctl/flightctl-catalog-collector-el9@" + digest,
		},
		{
			name: "When a digest-pinned reference also carries a registry port it should render the same reference back",
			// The registry port is a second colon, and it must not be the one
			// the reference is split at.
			reference:      "localhost:5000/flightctl/flightctl-catalog-collector@" + digest,
			wantRepository: "localhost:5000/flightctl/flightctl-catalog-collector@sha256",
			wantTag:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantImage:      "localhost:5000/flightctl/flightctl-catalog-collector@" + digest,
		},
		{
			name:           "When the reference carries a tag it should render the same reference back",
			reference:      "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123",
			wantRepository: "quay.io/flightctl/flightctl-catalog-collector-el9",
			wantTag:        "v0.9.0-123",
			wantImage:      "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123",
		},
		{
			name:           "When a tagged reference also carries a registry port it should render the same reference back",
			reference:      "localhost:5000/flightctl/flightctl-catalog-collector:v1",
			wantRepository: "localhost:5000/flightctl/flightctl-catalog-collector",
			wantTag:        "v1",
			wantImage:      "localhost:5000/flightctl/flightctl-catalog-collector:v1",
		},
		{
			name: "When an untagged reference carries a registry port it should render the port intact and default the tag",
			// The only colon is the port's, so there is no tag to take: the
			// rendered reference is the input plus ":latest", not the input.
			reference:      "localhost:5000/flightctl/flightctl-catalog-collector",
			wantRepository: "localhost:5000/flightctl/flightctl-catalog-collector",
			wantTag:        "latest",
			wantImage:      "localhost:5000/flightctl/flightctl-catalog-collector:latest",
		},
		{
			name:           "When a local reference has no tag it should render with the default tag",
			reference:      "localhost/flightctl-catalog-collector-el9",
			wantRepository: "localhost/flightctl-catalog-collector-el9",
			wantTag:        "latest",
			wantImage:      "localhost/flightctl-catalog-collector-el9:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository, tag := splitImageRef(tt.reference)
			if repository != tt.wantRepository {
				t.Errorf("image.image = %q, want %q", repository, tt.wantRepository)
			}
			if tag != tt.wantTag {
				t.Errorf("image.tag = %q, want %q", tag, tt.wantTag)
			}

			got := renderedCollectorImage(t, repository, tag)
			if got != tt.wantImage {
				t.Errorf("rendered collector image = %q, want %q (from %q via image.image=%q image.tag=%q)",
					got, tt.wantImage, tt.reference, repository, tag)
			}
		})
	}
}

package appinspection_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/helm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestInspectHelm(t *testing.T) {
	const manifest = "kind: Pod\nspec:\n  containers:\n  - image: quay.io/example/chart-app:v1\n"
	app := v1beta1.HelmApplication{Name: lo.ToPtr("release"), Namespace: lo.ToPtr("apps")}
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageSpec{Image: "oci://registry.example/charts/app:1.0.0"}))

	for _, tt := range []struct {
		name        string
		manifest    string
		stderr      string
		wantRefs    bool
		diagnostics []appinspection.Diagnostic
	}{
		{"When Helm renders on first deployment it should report workload refs", manifest, "", true, nil},
		{"When a Helm chart has no supported workloads it should return a complete empty result", "kind: Service\n", "", false, nil},
		{"When part of a rendered stream is unreadable it should retain earlier refs", manifest + "---\nspec: [private-value\n", "", true, []appinspection.Diagnostic{appinspection.DiagnosticParseFailed}},
		{"When the renderer fails it should report an incomplete result with a safe diagnostic", "", "private-value", false, []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec := &helmFixtureExecuter{t: t, manifest: tt.manifest, stderr: tt.stderr}
			result := appinspection.InspectHelm(t.Context(), helm.NewRenderer(exec, nil), app, nil, nil, nil)
			require.Equal(t, []string{"pull", "dependency", "template"}, exec.calls)
			require.Equal(t, len(tt.diagnostics) == 0, result.Complete())
			require.Equal(t, tt.diagnostics, result.Diagnostics)
			var refs []appinspection.Reference
			if tt.wantRefs {
				refs = []appinspection.Reference{{Image: "quay.io/example/chart-app:v1", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage}}
			}
			require.ElementsMatch(t, refs, result.References)
			diagnostic, err := json.Marshal(result.Diagnostics)
			require.NoError(t, err)
			require.NotContains(t, string(diagnostic), "private-value")
		})
	}
}

type helmFixtureExecuter struct {
	executer.Executer
	t        *testing.T
	manifest string
	stderr   string
	calls    []string
}

func (e *helmFixtureExecuter) ExecuteWithContextFromDir(_ context.Context, _ string, command string, args []string, _ ...string) (string, string, int) {
	require.Equal(e.t, "helm", command)
	e.calls = append(e.calls, args[0])
	if args[0] == "template" {
		require.Equal(e.t, "release", args[1])
		require.Contains(e.t, args, "apps")
		require.NotContains(e.t, args, "--is-upgrade")
		if e.stderr != "" {
			return "", e.stderr, 1
		}
		return e.manifest, "", 0
	}
	return "", "", 0
}

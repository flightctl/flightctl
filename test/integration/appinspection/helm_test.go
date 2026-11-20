package appinspection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/helm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestInspectHelmWithVendoredSubchart(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm CLI is unavailable")
	}
	app := v1beta1.HelmApplication{
		Name:        lo.ToPtr("target-release"),
		Namespace:   lo.ToPtr("target-namespace"),
		ValuesFiles: lo.ToPtr([]string{"target-values.yaml"}),
		Values:      lo.ToPtr(map[string]any{"tag": "inline"}),
	}
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageSpec{Image: "oci://registry.example/charts/app:0.1.0"}))

	cli := &localChartExecuter{Executer: executer.NewCommonExecuter(), t: t}
	result := appinspection.InspectHelm(t.Context(), helm.NewRenderer(cli, nil), app, nil, nil, nil)
	require.True(t, result.Complete(), "diagnostics: %v", result.Diagnostics)
	require.ElementsMatch(t, []appinspection.Reference{
		{Image: "quay.io/example/target-namespace/target-release:file-inline", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage},
		{Image: "quay.io/example/child:file", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage},
	}, result.References)
}

type localChartExecuter struct {
	executer.Executer
	t *testing.T
}

func (e *localChartExecuter) ExecuteWithContextFromDir(ctx context.Context, dir, command string, args []string, env ...string) (string, string, int) {
	require.Equal(e.t, "helm", command)
	if args[0] == "pull" {
		dest := slices.Index(args, "--destination")
		require.NotEqual(e.t, -1, dest)
		require.NoError(e.t, os.CopyFS(filepath.Join(args[dest+1], "app"), os.DirFS("testdata/app")))
		return "", "", 0
	}
	require.Contains(e.t, []string{"dependency", "template"}, args[0])
	stdout, stderr, code := e.Executer.ExecuteWithContextFromDir(ctx, dir, command, args, env...)
	require.Zero(e.t, code, "helm %s failed: %s", args[0], stderr)
	return stdout, stderr, code
}

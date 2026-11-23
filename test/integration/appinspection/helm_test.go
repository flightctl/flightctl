package appinspection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

	cli := &localChartExecuter{Executer: executer.NewCommonExecuter(), t: t, pullFrom: "testdata/app"}
	result := appinspection.InspectHelm(t.Context(), helm.NewRenderer(cli, nil), app, nil, nil, nil)
	require.True(t, result.Complete(), "diagnostics: %v", result.Diagnostics)
	require.ElementsMatch(t, []appinspection.Reference{
		{Image: "quay.io/example/target-namespace/target-release:file-inline", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage},
		{Image: "quay.io/example/child:file", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage},
	}, result.References)
}

func TestInspectHelmWhenChildChangesUnderSameParent(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm CLI is unavailable")
	}
	root := t.TempDir()
	parent, child := filepath.Join(root, "app"), filepath.Join(root, "child")
	require.NoError(t, os.CopyFS(child, os.DirFS("testdata/app/charts/child")))
	require.NoError(t, os.CopyFS(filepath.Join(parent, "templates"), os.DirFS("testdata/app/templates")))
	chart, err := os.ReadFile("testdata/app/Chart.yaml")
	require.NoError(t, err)
	chart = append(chart, []byte("    repository: file://"+child+"\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "Chart.yaml"), chart, 0o600))
	app := v1beta1.HelmApplication{Values: lo.ToPtr(map[string]any{"tag": "initial", "channel": "stable", "child": map[string]any{"tag": "initial"}})}
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageSpec{Image: "oci://registry.example/charts/app:0.1.0"}))
	renderer := helm.NewRenderer(&localChartExecuter{Executer: executer.NewCommonExecuter(), t: t, pullFrom: parent}, nil)
	first := appinspection.InspectHelm(t.Context(), renderer, app, nil, nil, nil)
	require.True(t, first.Complete(), "diagnostics: %v", first.Diagnostics)
	require.Contains(t, first.References, appinspection.Reference{Image: "quay.io/example/child:initial", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage})

	podPath := filepath.Join(child, "templates", "pod.yaml")
	template, err := os.ReadFile(podPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(podPath, []byte(strings.ReplaceAll(string(template), "{{ .Values.tag }}", "updated")), 0o600))
	updated := appinspection.InspectHelm(t.Context(), renderer, app, nil, nil, nil)
	require.True(t, updated.Complete(), "diagnostics: %v", updated.Diagnostics)
	require.Contains(t, updated.References, appinspection.Reference{Image: "quay.io/example/child:updated", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerHelmImage})
}

type localChartExecuter struct {
	executer.Executer
	t        *testing.T
	pullFrom string
}

func (e *localChartExecuter) ExecuteWithContextFromDir(ctx context.Context, dir, command string, args []string, env ...string) (string, string, int) {
	require.Equal(e.t, "helm", command)
	if args[0] == "pull" {
		dest := slices.Index(args, "--destination")
		require.NotEqual(e.t, -1, dest)
		require.NoError(e.t, os.CopyFS(filepath.Join(args[dest+1], "app"), os.DirFS(e.pullFrom)))
		return "", "", 0
	}
	require.Contains(e.t, []string{"dependency", "template"}, args[0])
	stdout, stderr, code := e.Executer.ExecuteWithContextFromDir(ctx, dir, command, args, env...)
	require.Zero(e.t, code, "helm %s failed: %s", args[0], stderr)
	return stdout, stderr, code
}

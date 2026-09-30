package helm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type helmTestExecuter struct {
	executer.Executer
	t             *testing.T
	chartName     string
	manifest      string
	templateError string
	calls         map[string][][]string
	templateArgs  []string
	templateFiles []string
	templateData  [][]byte
}

func (e *helmTestExecuter) ExecuteWithContextFromDir(_ context.Context, _ string, command string, args []string, _ ...string) (string, string, int) {
	e.t.Helper()
	require.Equal(e.t, "helm", command)
	args = append([]string(nil), args...)
	e.calls[args[0]] = append(e.calls[args[0]], args)
	switch args[0] {
	case "pull":
		destination := argumentValue(args, "--destination")
		require.NotEmpty(e.t, destination)
		registryConfigPath := argumentValue(args, "--registry-config")
		require.NotEmpty(e.t, registryConfigPath)
		registryConfig, err := os.ReadFile(registryConfigPath)
		require.NoError(e.t, err)
		require.JSONEq(e.t, `{"auths":{"registry.example":{}}}`, string(registryConfig))
		chartPath := filepath.Join(destination, e.chartName)
		require.NoError(e.t, os.MkdirAll(chartPath, 0o700))
		for _, name := range []string{"values-prod.yaml", "values-override.yaml"} {
			require.NoError(e.t, os.WriteFile(filepath.Join(chartPath, name), []byte(name+"\n"), 0o600))
		}
	case "template":
		e.templateArgs = args
		for i, arg := range args {
			if arg == "--values" && i+1 < len(args) {
				e.templateFiles = append(e.templateFiles, args[i+1])
				contents, err := os.ReadFile(args[i+1])
				require.NoError(e.t, err)
				e.templateData = append(e.templateData, contents)
			}
		}
		if e.templateError != "" {
			return "", e.templateError, 1
		}
		return e.manifest, "", 0
	case "dependency":
		repositoryConfigPath := argumentValue(args, "--repository-config")
		require.NotEmpty(e.t, repositoryConfigPath)
		repositoryConfig, err := os.ReadFile(repositoryConfigPath)
		require.NoError(e.t, err)
		require.Equal(e.t, "repositories: []\n", string(repositoryConfig))
	default:
		require.FailNow(e.t, "unexpected helm command", args[0])
	}
	return "", "", 0
}

func TestRendererImageRefs(t *testing.T) {
	app := v1beta1.HelmApplication{
		AppType:     v1beta1.AppTypeHelm,
		Name:        lo.ToPtr("release"),
		Namespace:   lo.ToPtr("apps"),
		ValuesFiles: lo.ToPtr([]string{"values-prod.yaml", "values-override.yaml"}),
		Values:      lo.ToPtr(map[string]interface{}{"replicaCount": 2}),
	}
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageSpec{Image: "oci://registry.example/charts/app:1.2.3"}))
	device := &domain.Device{Status: &domain.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "release"}}}}
	manifest := strings.Join([]string{
		"apiVersion: v1",
		"kind: Pod",
		"spec:",
		"  containers:",
		"    - image: quay.io/acme/web:v2",
		"---",
		"apiVersion: apps/v1",
		"kind: Deployment",
		"spec:",
		"  template:",
		"    spec:",
		"      initContainers:",
		"        - image: quay.io/acme/init:v1",
		"      containers:",
		"        - image: quay.io/acme/web:v2",
	}, "\n")
	exec := &helmTestExecuter{
		t:         t,
		chartName: "app",
		manifest:  manifest,
		calls:     make(map[string][][]string),
	}
	renderer := NewRenderer(exec, nil)
	scheme := domain.OciRepoSchemeHttp
	registrySpec := &domain.OciRepoSpec{Registry: "registry.example", Scheme: &scheme}
	config, err := json.Marshal([]domain.FileSpec{
		{Path: helmRegistryConfig, Content: `{"auths":{"registry.example":{}}}`},
		{Path: helmRepositoryConfig, Content: "repositories: []\n"},
	})
	require.NoError(t, err)

	images, err := renderer.ImageRefs(context.Background(), app, device, config, registrySpec)
	require.NoError(t, err)
	require.Equal(t, []string{"quay.io/acme/init:v1", "quay.io/acme/web:v2"}, images)

	require.Len(t, exec.calls["pull"], 1)
	pullArgs := exec.calls["pull"][0]
	require.Contains(t, pullArgs, "oci://registry.example/charts/app")
	require.Contains(t, pullArgs, "1.2.3")
	require.Contains(t, pullArgs, "--plain-http")
	require.Contains(t, pullArgs, "--registry-config")
	require.Len(t, exec.calls["dependency"], 1)
	require.Contains(t, exec.calls["dependency"][0], "--repository-config")
	require.Len(t, exec.calls["template"], 1)
	require.Equal(t, "template", exec.templateArgs[0])
	require.Contains(t, exec.templateArgs, "--is-upgrade")
	require.Contains(t, exec.templateArgs, "--namespace")
	require.Equal(t, []string{"values-prod.yaml", "values-override.yaml"}, []string{
		filepath.Base(exec.templateFiles[0]),
		filepath.Base(exec.templateFiles[1]),
	})
	require.Len(t, exec.templateFiles, 3, "inline values should be applied after chart-relative files")
	require.Contains(t, string(exec.templateData[2]), "replicaCount: 2")
}

func TestRendererImageRefsReturnsTemplateError(t *testing.T) {
	app := v1beta1.HelmApplication{
		AppType: v1beta1.AppTypeHelm,
		Name:    lo.ToPtr("release"),
	}
	require.NoError(t, app.FromImageApplicationProviderSpec(v1beta1.ImageSpec{Image: "oci://registry.example/charts/app:1.2.3"}))
	device := &domain.Device{Status: &domain.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "release"}}}}
	exec := &helmTestExecuter{
		t:             t,
		chartName:     "app",
		templateError: "invalid chart values",
		calls:         make(map[string][][]string),
	}
	renderer := NewRenderer(exec, nil)
	scheme := domain.OciRepoSchemeHttp
	registrySpec := &domain.OciRepoSpec{Registry: "registry.example", Scheme: &scheme}
	config, err := json.Marshal([]domain.FileSpec{
		{Path: helmRegistryConfig, Content: `{"auths":{"registry.example":{}}}`},
		{Path: helmRepositoryConfig, Content: "repositories: []\n"},
	})
	require.NoError(t, err)

	images, err := renderer.ImageRefs(context.Background(), app, device, config, registrySpec)

	require.Nil(t, images)
	require.ErrorContains(t, err, "render target chart")
	require.ErrorContains(t, err, "invalid chart values")
	require.Len(t, exec.calls["template"], 1)
}

func argumentValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

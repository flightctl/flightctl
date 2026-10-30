package tenant_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type renderedResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	RoleRef struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"roleRef"`
	Subjects []struct {
		Kind      string `yaml:"kind"`
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"subjects"`
}

func chartDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to determine chart test path")
	return filepath.Dir(filename)
}

func renderTenantChart(t *testing.T, values string) ([]renderedResource, string, error) {
	t.Helper()

	helmPath, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; make lint-helm runs the chart rendering checks")
	}

	args := []string{
		"template", "flightctl-tenant", chartDir(t),
		"--namespace", "project-a",
	}
	if values != "" {
		path := filepath.Join(t.TempDir(), "values.yaml")
		require.NoError(t, os.WriteFile(path, []byte(values), 0o600))
		args = append(args, "--values", path)
	}

	output, err := exec.Command(helmPath, args...).CombinedOutput()
	if err != nil {
		return nil, string(output), err
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(output)))
	var resources []renderedResource
	for {
		var resource renderedResource
		if err := decoder.Decode(&resource); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode helm output: %v", err)
		}
		if resource.Kind != "" {
			resources = append(resources, resource)
		}
	}
	return resources, string(output), nil
}

func find(resources []renderedResource, kind, name string) []renderedResource {
	var found []renderedResource
	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			found = append(found, resource)
		}
	}
	return found
}

func TestTenantBundleRendersNamespacedRBAC(t *testing.T) {
	values := `
flightctlNamespace: flightctl
flightctlReleaseName: flightctl
roleBindings:
  - role: org-admin
    subjects:
      - kind: Group
        name: platform-admins
  - role: viewer
    subjects:
      - kind: User
        name: alice
`
	resources, output, err := renderTenantChart(t, values)
	require.NoError(t, err, output)

	for _, roleName := range []string{
		"flightctl-org-admin-flightctl",
		"flightctl-operator-flightctl",
		"flightctl-viewer-flightctl",
		"flightctl-installer-flightctl",
	} {
		roles := find(resources, "Role", roleName)
		require.Len(t, roles, 1, "expected Role %s", roleName)
		require.Equal(t, "project-a", roles[0].Metadata.Namespace)
	}

	adminBinding := find(resources, "RoleBinding", "flightctl-org-admin-tenant")
	require.Len(t, adminBinding, 1)
	require.Equal(t, "Role", adminBinding[0].RoleRef.Kind)
	require.Equal(t, "flightctl-org-admin-flightctl", adminBinding[0].RoleRef.Name)

	viewerBinding := find(resources, "RoleBinding", "flightctl-viewer-tenant")
	require.Len(t, viewerBinding, 1)
	require.Equal(t, "flightctl-viewer-flightctl", viewerBinding[0].RoleRef.Name)

	readerBinding := find(resources, "RoleBinding", "flightctl-auth-reader")
	require.Len(t, readerBinding, 1)
	require.Equal(t, "flightctl-auth-reader", readerBinding[0].RoleRef.Name)
	require.ElementsMatch(t, []struct {
		Kind      string `yaml:"kind"`
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	}{
		{Kind: "ServiceAccount", Name: "flightctl-api", Namespace: "flightctl"},
		{Kind: "ServiceAccount", Name: "flightctl-imagebuilder-api", Namespace: "flightctl"},
		{Kind: "ServiceAccount", Name: "flightctl-alertmanager-proxy", Namespace: "flightctl"},
		{Kind: "ServiceAccount", Name: "flightctl-remote-access", Namespace: "flightctl"},
	}, readerBinding[0].Subjects)

	for _, resource := range resources {
		require.NotEqual(t, "ClusterRole", resource.Kind, "tenant bundle must not render cluster-scoped resources")
		require.NotEqual(t, "ClusterRoleBinding", resource.Kind, "tenant bundle must not render cluster-scoped resources")
	}
}

func TestTenantBundleRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		values  string
		wantErr string
	}{
		{
			name:    "When flightctlNamespace is empty it should fail",
			values:  "flightctlNamespace: \"\"\n",
			wantErr: "flightctlNamespace",
		},
		{
			name: "When a role is unknown it should fail",
			values: `
flightctlNamespace: flightctl
roleBindings:
  - role: owner
    subjects:
      - kind: User
        name: alice
`,
			wantErr: "roleBindings[].role",
		},
		{
			name: "When a role binding has no subjects it should fail",
			values: `
flightctlNamespace: flightctl
roleBindings:
  - role: org-admin
    subjects: []
`,
			wantErr: "subjects must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, output, err := renderTenantChart(t, tt.values)
			require.Error(t, err, output)
			require.Contains(t, output, tt.wantErr)
		})
	}
}

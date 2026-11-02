package flightctl_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type renderedResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data    map[string]string `yaml:"data"`
	RoleRef struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"roleRef"`
	Subjects []roleSubject `yaml:"subjects"`
	Rules    []struct {
		APIGroups []string `yaml:"apiGroups"`
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	} `yaml:"rules"`
}

type roleSubject struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

func testChartDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to determine chart test path")
	return filepath.Dir(filename)
}

func renderNamespaceScopedChart(t *testing.T, values map[string]string, extraArgs ...string) ([]renderedResource, string, error) {
	t.Helper()

	helmPath, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; make lint-helm runs the chart rendering checks")
	}

	chartDir := testChartDir(t)
	args := []string{
		"template", "flightctl", chartDir,
		"--namespace", "flightctl",
		"--values", filepath.Join(chartDir, "lint-values.yaml"),
		"--disable-openapi-validation",
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--set", key+"="+values[key])
	}
	args = append(args, extraArgs...)

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

func findResources(resources []renderedResource, kind, name string) []renderedResource {
	var found []renderedResource
	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			found = append(found, resource)
		}
	}
	return found
}

func hasRule(resource renderedResource, apiGroup, resourceName string, verbs ...string) bool {
	for _, rule := range resource.Rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != apiGroup {
			continue
		}
		hasResource := false
		for _, name := range rule.Resources {
			if name == resourceName {
				hasResource = true
				break
			}
		}
		if !hasResource {
			continue
		}
		for _, want := range verbs {
			hasVerb := false
			for _, verb := range rule.Verbs {
				if verb == want {
					hasVerb = true
					break
				}
			}
			if !hasVerb {
				return false
			}
		}
		return true
	}
	return false
}

func TestNamespaceScopedAPIRBAC(t *testing.T) {
	baseValues := map[string]string{"global.auth.type": "aap"}

	t.Run("When namespaceScoped is true it should render namespaced API RBAC", func(t *testing.T) {
		values := map[string]string{"global.namespaceScoped": "true"}
		for key, value := range baseValues {
			values[key] = value
		}
		resources, output, err := renderNamespaceScopedChart(t, values)
		require.NoError(t, err, output)

		require.Empty(t, findResources(resources, "ClusterRole", "flightctl-api-flightctl"))
		require.Empty(t, findResources(resources, "ClusterRoleBinding", "flightctl-api-flightctl"))

		roles := findResources(resources, "Role", "flightctl-api-flightctl")
		require.Len(t, roles, 1)
		require.Equal(t, "flightctl", roles[0].Metadata.Namespace)
		require.True(t, hasRule(roles[0], "rbac.authorization.k8s.io", "rolebindings", "get", "list"))
		require.False(t, hasRule(roles[0], "authentication.k8s.io", "tokenreviews", "create"))

		bindings := findResources(resources, "RoleBinding", "flightctl-api-flightctl")
		require.Len(t, bindings, 1)
		require.Equal(t, "flightctl", bindings[0].Metadata.Namespace)
		require.Equal(t, "Role", bindings[0].RoleRef.Kind)
		require.Equal(t, "flightctl-api-flightctl", bindings[0].RoleRef.Name)
		require.Contains(t, bindings[0].Subjects, roleSubject{Kind: "ServiceAccount", Name: "flightctl-api", Namespace: "flightctl"})
	})

	t.Run("When namespaceScoped is false it should keep the API ClusterRole and ClusterRoleBinding", func(t *testing.T) {
		resources, output, err := renderNamespaceScopedChart(t, map[string]string{"global.auth.type": "k8s"})
		require.NoError(t, err, output)

		require.Empty(t, findResources(resources, "Role", "flightctl-api-flightctl"))
		require.Empty(t, findResources(resources, "RoleBinding", "flightctl-api-flightctl"))

		roles := findResources(resources, "ClusterRole", "flightctl-api-flightctl")
		require.Len(t, roles, 1)
		require.True(t, hasRule(roles[0], "rbac.authorization.k8s.io", "rolebindings", "get", "list"))

		bindings := findResources(resources, "ClusterRoleBinding", "flightctl-api-flightctl")
		require.Len(t, bindings, 1)
		require.Equal(t, "ClusterRole", bindings[0].RoleRef.Kind)
	})
}

func TestNamespaceScopedComponentRBAC(t *testing.T) {
	values := map[string]string{
		"global.auth.type":       "k8s",
		"global.namespaceScoped": "true",
	}

	resources, output, err := renderNamespaceScopedChart(t, values)
	require.NoError(t, err, output)

	require.Empty(t, findResources(resources, "ClusterRole", "flightctl-imagebuilder-api-flightctl"))
	require.Empty(t, findResources(resources, "ClusterRoleBinding", "flightctl-imagebuilder-api-flightctl"))
	require.Empty(t, findResources(resources, "ClusterRole", "flightctl-alertmanager-proxy-flightctl"))
	require.Empty(t, findResources(resources, "ClusterRoleBinding", "flightctl-alertmanager-proxy-flightctl"))
	require.Empty(t, findResources(resources, "ClusterRole", "flightctl-remote-access-flightctl"))
	require.Empty(t, findResources(resources, "ClusterRoleBinding", "flightctl-remote-access-flightctl"))

	imageBuilderRoles := findResources(resources, "Role", "flightctl-imagebuilder-api-flightctl")
	require.Len(t, imageBuilderRoles, 1)
	require.Equal(t, "flightctl", imageBuilderRoles[0].Metadata.Namespace)
	require.True(t, hasRule(imageBuilderRoles[0], "rbac.authorization.k8s.io", "rolebindings", "get", "list"))
	require.False(t, hasRule(imageBuilderRoles[0], "authentication.k8s.io", "tokenreviews", "create"))
	imageBuilderBindings := findResources(resources, "RoleBinding", "flightctl-imagebuilder-api-flightctl")
	require.Len(t, imageBuilderBindings, 1)
	require.Equal(t, "flightctl", imageBuilderBindings[0].Metadata.Namespace)
	require.Equal(t, "Role", imageBuilderBindings[0].RoleRef.Kind)

	alertmanagerRoles := findResources(resources, "Role", "flightctl-alertmanager-proxy-flightctl")
	require.Len(t, alertmanagerRoles, 1)
	require.Equal(t, "flightctl", alertmanagerRoles[0].Metadata.Namespace)
	require.True(t, hasRule(alertmanagerRoles[0], "rbac.authorization.k8s.io", "rolebindings", "get", "list"))
	require.False(t, hasRule(alertmanagerRoles[0], "authentication.k8s.io", "tokenreviews", "create"))
	require.False(t, hasRule(alertmanagerRoles[0], "authorization.k8s.io", "selfsubjectaccessreviews", "create"))
	alertmanagerBindings := findResources(resources, "RoleBinding", "flightctl-alertmanager-proxy-flightctl")
	require.Len(t, alertmanagerBindings, 1)
	require.Equal(t, "flightctl", alertmanagerBindings[0].Metadata.Namespace)
	require.Equal(t, "Role", alertmanagerBindings[0].RoleRef.Kind)

	remoteAccessRoles := findResources(resources, "Role", "flightctl-remote-access-flightctl")
	require.Len(t, remoteAccessRoles, 1)
	require.Equal(t, "flightctl", remoteAccessRoles[0].Metadata.Namespace)
	require.True(t, hasRule(remoteAccessRoles[0], "rbac.authorization.k8s.io", "rolebindings", "get", "list"))
	remoteAccessBindings := findResources(resources, "RoleBinding", "flightctl-remote-access-flightctl")
	require.Len(t, remoteAccessBindings, 1)
	require.Equal(t, "flightctl", remoteAccessBindings[0].Metadata.Namespace)
	require.Equal(t, "Role", remoteAccessBindings[0].RoleRef.Kind)
}

func TestNamespaceScopedDefaultUserRoles(t *testing.T) {
	values := map[string]string{
		"global.auth.type":       "k8s",
		"global.namespaceScoped": "true",
	}

	resources, output, err := renderNamespaceScopedChart(t, values)
	require.NoError(t, err, output)

	for _, roleName := range []string{"flightctl-admin-flightctl", "flightctl-org-admin-flightctl", "flightctl-viewer-flightctl", "flightctl-operator-flightctl", "flightctl-installer-flightctl"} {
		require.Empty(t, findResources(resources, "ClusterRole", roleName))
		roles := findResources(resources, "Role", roleName)
		require.Len(t, roles, 1, "expected Role %s", roleName)
		require.Equal(t, "flightctl", roles[0].Metadata.Namespace)
	}

	bindings := findResources(resources, "RoleBinding", "flightctl-admin-binding")
	require.Len(t, bindings, 1)
	require.Equal(t, "Role", bindings[0].RoleRef.Kind)
	require.Equal(t, "flightctl-admin-flightctl", bindings[0].RoleRef.Name)
}

func TestDefaultOpenShiftAdminBinding(t *testing.T) {
	baseValues := map[string]string{
		"global.auth.type":                       "openshift",
		"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
		"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
		"global.auth.openshift.issuer":           "https://oauth.example.com",
		"global.auth.openshift.clientId":         "flightctl-test",
		"global.auth.openshift.clientSecret":     "test-secret",
	}
	args := []string{"--api-versions", "route.openshift.io/v1"}

	t.Run("When namespaceScoped is true it should bind the default admin to org-admin", func(t *testing.T) {
		values := map[string]string{"global.namespaceScoped": "true"}
		for key, value := range baseValues {
			values[key] = value
		}
		resources, output, err := renderNamespaceScopedChart(t, values, args...)
		require.NoError(t, err, output)

		bindings := findResources(resources, "RoleBinding", "flightctl-admin-binding")
		require.Len(t, bindings, 1)
		require.Equal(t, "Role", bindings[0].RoleRef.Kind)
		require.Equal(t, "flightctl-org-admin-flightctl", bindings[0].RoleRef.Name)
	})

	t.Run("When namespaceScoped is false it should keep the default admin global", func(t *testing.T) {
		resources, output, err := renderNamespaceScopedChart(t, baseValues, args...)
		require.NoError(t, err, output)

		bindings := findResources(resources, "RoleBinding", "flightctl-admin-binding")
		require.Len(t, bindings, 1)
		require.Equal(t, "ClusterRole", bindings[0].RoleRef.Kind)
		require.Equal(t, "flightctl-admin-flightctl", bindings[0].RoleRef.Name)
	})
}

func TestOpenShiftServiceAccountOAuthClient(t *testing.T) {
	values := map[string]string{
		"global.auth.type":                       "openshift",
		"global.auth.openshift.clientType":       "serviceaccount",
		"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
		"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
		"global.auth.openshift.issuer":           "https://oauth.example.com",
	}

	resources, output, err := renderNamespaceScopedChart(t, values)
	require.NoError(t, err, output)

	for _, resource := range resources {
		require.NotEqual(t, "OAuthClient", resource.Kind, "service-account clients must not render a cluster-scoped OAuthClient")
	}

	serviceAccounts := findResources(resources, "ServiceAccount", "flightctl-api")
	require.Len(t, serviceAccounts, 1)
	require.Equal(t, "https://ui.flightctl.github.io/callback", serviceAccounts[0].Metadata.Annotations["serviceaccounts.openshift.io/oauth-redirecturi.ui"])
	require.Equal(t, "http://127.0.0.1/callback", serviceAccounts[0].Metadata.Annotations["serviceaccounts.openshift.io/oauth-redirecturi.cli"])

	configMaps := findResources(resources, "ConfigMap", "flightctl-api-config")
	require.Len(t, configMaps, 1)
	config := configMaps[0].Data["config.yaml"]
	require.Contains(t, config, "clientId: system:serviceaccount:flightctl:flightctl-api")
	require.Contains(t, config, "clientType: serviceaccount")
	require.Contains(t, config, "clientSecretFile: /var/run/secrets/kubernetes.io/serviceaccount/token")
	require.Contains(t, config, "user:info")
	require.Contains(t, config, "user:check-access")
	require.Contains(t, config, "user:list-projects")
	require.NotContains(t, config, "clientSecret:")
}

func TestNamespaceScopedOpenShiftResources(t *testing.T) {
	values := map[string]string{
		"global.auth.type":                       "openshift",
		"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
		"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
		"global.auth.openshift.issuer":           "https://oauth.example.com",
		"global.auth.openshift.clientId":         "flightctl-test",
		"global.auth.openshift.clientSecret":     "test-secret",
		"global.namespaceScoped":                 "true",
	}
	args := []string{
		"--set-string", "global.enableOpenShiftExtensions=true",
		"--api-versions", "route.openshift.io/v1",
		"--api-versions", "security.openshift.io/v1",
		"--api-versions", "config.openshift.io/v1",
		"--api-versions", "multicluster.openshift.io/v1",
	}

	resources, output, err := renderNamespaceScopedChart(t, values, args...)
	require.NoError(t, err, output)

	for _, resource := range resources {
		switch resource.Kind {
		case "ClusterRole", "ClusterRoleBinding", "SecurityContextConstraints", "OAuthClient",
			"ConsolePlugin", "ConsoleCLIDownload", "Namespace", "CustomResourceDefinition",
			"ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "APIService":
			t.Errorf("namespace-scoped install must not render %s %s", resource.Kind, resource.Metadata.Name)
		}
	}
}

func TestClusterScopedOpenShiftResourcesRemainByDefault(t *testing.T) {
	values := map[string]string{
		"global.auth.type":                       "openshift",
		"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
		"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
		"global.auth.openshift.issuer":           "https://oauth.example.com",
		"global.auth.openshift.clientId":         "flightctl-test",
		"global.auth.openshift.clientSecret":     "test-secret",
	}
	args := []string{
		"--set-string", "global.enableOpenShiftExtensions=true",
		"--set-string", "global.enableMulticlusterExtensions=true",
		"--api-versions", "route.openshift.io/v1",
		"--api-versions", "security.openshift.io/v1",
		"--api-versions", "config.openshift.io/v1",
		"--api-versions", "console.openshift.io/v1",
	}

	resources, output, err := renderNamespaceScopedChart(t, values, args...)
	require.NoError(t, err, output)

	require.Len(t, findResources(resources, "OAuthClient", "flightctl-test"), 1)
	require.Empty(t, findResources(resources, "SecurityContextConstraints", "flightctl-imagebuilder-worker"))
	require.Len(t, findResources(resources, "SecurityContextConstraints", "flightctl-delta-worker"), 1)
	require.Len(t, findResources(resources, "ClusterRole", "flightctl-device-registration"), 1)
	require.Len(t, findResources(resources, "ConsolePlugin", "flightctl-plugin"), 1)
}

func TestNamespaceScopedRejectsClusterOnlyFeatures(t *testing.T) {
	baseValues := map[string]string{
		"global.auth.type":       "aap",
		"global.namespaceScoped": "true",
	}

	tests := []struct {
		name        string
		values      map[string]string
		extraArgs   []string
		wantErrText string
	}{
		{
			name:        "When worker clusterLevelSecretAccess is enabled it should fail",
			values:      map[string]string{"worker.clusterLevelSecretAccess": "true"},
			wantErrText: "worker.clusterLevelSecretAccess",
		},
		{
			name:        "When periodic clusterLevelSecretAccess is enabled it should fail",
			values:      map[string]string{"periodic.clusterLevelSecretAccess": "true"},
			wantErrText: "periodic.clusterLevelSecretAccess",
		},
		{
			name:        "When MultiCluster Engine extensions are enabled it should fail",
			extraArgs:   []string{"--set-string", "global.enableMulticlusterExtensions=true"},
			wantErrText: "global.namespaceScoped=true",
		},
		{
			name:        "When baseDomain is empty it should fail without a lookup",
			values:      map[string]string{"global.baseDomain": ""},
			wantErrText: "global.baseDomain is required",
		},
		{
			name: "When OpenShift auth has no client secret it should fail without a lookup",
			values: map[string]string{
				"global.auth.type":                       "openshift",
				"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
				"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
				"global.auth.openshift.issuer":           "https://oauth.example.com",
				"global.auth.openshift.clientId":         "flightctl-test",
			},
			extraArgs:   []string{"--api-versions", "route.openshift.io/v1"},
			wantErrText: "global.auth.openshift.clientSecret",
		},
		{
			name: "When OpenShift auth has no endpoints it should fail without a lookup",
			values: map[string]string{
				"global.auth.type":                   "openshift",
				"global.auth.openshift.clientId":     "flightctl-test",
				"global.auth.openshift.clientSecret": "test-secret",
			},
			extraArgs:   []string{"--api-versions", "route.openshift.io/v1"},
			wantErrText: "cannot look up the oauth-openshift route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range baseValues {
				values[key] = value
			}
			for key, value := range tt.values {
				values[key] = value
			}
			_, output, err := renderNamespaceScopedChart(t, values, tt.extraArgs...)
			require.Error(t, err, output)
			require.Contains(t, output, tt.wantErrText)
		})
	}
}

func TestNamespaceScopedAvoidsOutOfNamespaceLookups(t *testing.T) {
	var mu sync.Mutex
	var outOfNamespaceRequests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveHelmDiscovery(w, r) {
			return
		}
		if isOutOfNamespaceResourcePath(r.URL.Path) {
			mu.Lock()
			outOfNamespaceRequests = append(outOfNamespaceRequests, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]any{
				"apiVersion": "v1",
				"kind":       "Status",
				"status":     "Failure",
				"message":    "forbidden: cannot get cluster-scoped resource",
				"reason":     "Forbidden",
				"code":       http.StatusForbidden,
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	serverArgs := []string{
		"--dry-run=server",
		"--kube-apiserver", server.URL,
		"--kube-token", "test",
	}

	t.Run("When namespaceScoped is true it should not look up resources outside the release namespace", func(t *testing.T) {
		values := map[string]string{
			"global.auth.type":                       "openshift",
			"global.auth.openshift.authorizationUrl": "https://oauth.example.com/oauth/authorize",
			"global.auth.openshift.tokenUrl":         "https://oauth.example.com/oauth/token",
			"global.auth.openshift.issuer":           "https://oauth.example.com",
			"global.auth.openshift.clientId":         "flightctl-test",
			"global.auth.openshift.clientSecret":     "test-secret",
			"global.namespaceScoped":                 "true",
		}
		args := append([]string{
			"--set-string", "global.enableOpenShiftExtensions=true",
			"--api-versions", "route.openshift.io/v1",
			"--api-versions", "config.openshift.io/v1",
			"--api-versions", "oauth.openshift.io/v1",
			"--api-versions", "multicluster.openshift.io/v1",
		}, serverArgs...)

		_, output, err := renderNamespaceScopedChart(t, values, args...)
		require.NoError(t, err, output)

		mu.Lock()
		defer mu.Unlock()
		require.Empty(t, outOfNamespaceRequests, "namespace-scoped install must not look up resources outside the release namespace")
	})

	t.Run("When namespaceScoped is false it should look up resources outside the release namespace", func(t *testing.T) {
		mu.Lock()
		outOfNamespaceRequests = nil
		mu.Unlock()

		values := map[string]string{"global.auth.type": "openshift"}
		args := append([]string{
			"--set-string", "global.enableOpenShiftExtensions=true",
			"--api-versions", "route.openshift.io/v1",
		}, serverArgs...)

		_, output, err := renderNamespaceScopedChart(t, values, args...)
		require.Error(t, err, output)

		mu.Lock()
		defer mu.Unlock()
		require.Contains(t, strings.Join(outOfNamespaceRequests, "\n"), "/api/v1/namespaces/openshift-config-managed/configmaps/default-ingress-cert")
	})
}

func TestNamespaceScopedCanDisablePrivilegedWorkers(t *testing.T) {
	values := map[string]string{
		"global.auth.type":           "aap",
		"global.namespaceScoped":     "true",
		"deltaWorker.enabled":        "false",
		"imageBuilderWorker.enabled": "false",
	}

	resources, output, err := renderNamespaceScopedChart(t, values)
	require.NoError(t, err, output)

	for _, resource := range resources {
		if strings.HasPrefix(resource.Metadata.Name, "flightctl-delta-worker") {
			t.Errorf("delta worker must not render when deltaWorker.enabled=false: %s %s", resource.Kind, resource.Metadata.Name)
		}
		if strings.HasPrefix(resource.Metadata.Name, "flightctl-imagebuilder-worker") {
			t.Errorf("imagebuilder worker must not render when imageBuilderWorker.enabled=false: %s %s", resource.Kind, resource.Metadata.Name)
		}
	}
}

func TestChartDoesNotRenderTokenReviewRBAC(t *testing.T) {
	tests := []struct {
		name     string
		authType string
	}{
		{name: "When auth type is k8s it should not render token review RBAC", authType: "k8s"},
		{name: "When auth type is openshift it should not render token review RBAC", authType: "openshift"},
		{name: "When auth type is oidc it should not render token review RBAC", authType: "oidc"},
		{name: "When auth type is aap it should not render token review RBAC", authType: "aap"},
		{name: "When auth type is none it should not render token review RBAC", authType: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{"global.auth.type": tt.authType}
			args := []string{}
			if tt.authType == "openshift" {
				values["global.auth.openshift.authorizationUrl"] = "https://oauth.example.com/oauth/authorize"
				values["global.auth.openshift.tokenUrl"] = "https://oauth.example.com/oauth/token"
				values["global.auth.openshift.issuer"] = "https://oauth.example.com"
				values["global.auth.openshift.clientSecret"] = "test-secret"
				args = append(args, "--api-versions", "route.openshift.io/v1")
			}

			resources, output, err := renderNamespaceScopedChart(t, values, args...)
			require.NoError(t, err, output)

			foundAPIRole := false
			for _, resource := range resources {
				if resource.Kind != "Role" && resource.Kind != "ClusterRole" {
					continue
				}
				require.False(t, hasRule(resource, "authentication.k8s.io", "tokenreviews", "create"), "%s %s must not grant token reviews", resource.Kind, resource.Metadata.Name)
				if resource.Metadata.Name == "flightctl-api-flightctl" {
					foundAPIRole = true
				}
			}
			require.True(t, foundAPIRole, "expected the API role to render")
		})
	}

	t.Run("When namespaceScoped is true it should not render token review RBAC", func(t *testing.T) {
		resources, output, err := renderNamespaceScopedChart(t, map[string]string{
			"global.auth.type":       "k8s",
			"global.namespaceScoped": "true",
		})
		require.NoError(t, err, output)
		for _, resource := range resources {
			if resource.Kind != "Role" && resource.Kind != "ClusterRole" {
				continue
			}
			require.False(t, hasRule(resource, "authentication.k8s.io", "tokenreviews", "create"), "%s %s must not grant token reviews", resource.Kind, resource.Metadata.Name)
		}
	})
}

func serveHelmDiscovery(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/version":
		writeJSON(w, map[string]any{"gitVersion": "v1.32.0"})
		return true
	case "/api":
		writeJSON(w, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		return true
	case "/apis":
		writeJSON(w, map[string]any{"kind": "APIGroupList", "groups": []map[string]any{
			apiGroup("apps"),
			apiGroup("batch"),
			apiGroup("cert-manager.io"),
			apiGroup("config.openshift.io"),
			apiGroup("networking.k8s.io"),
			apiGroup("oauth.openshift.io"),
			apiGroup("rbac.authorization.k8s.io"),
			apiGroup("route.openshift.io"),
			apiGroup("security.openshift.io"),
			apiGroup("multicluster.openshift.io"),
		}})
		return true
	case "/api/v1":
		writeJSON(w, apiResourceList("v1", map[string]string{
			"configmaps":             "ConfigMap",
			"persistentvolumeclaims": "PersistentVolumeClaim",
			"pods":                   "Pod",
			"secrets":                "Secret",
			"serviceaccounts":        "ServiceAccount",
			"services":               "Service",
		}))
		return true
	case "/apis/apps/v1":
		writeJSON(w, apiResourceList("apps/v1", map[string]string{
			"deployments":  "Deployment",
			"statefulsets": "StatefulSet",
		}))
		return true
	case "/apis/batch/v1":
		writeJSON(w, apiResourceList("batch/v1", map[string]string{"jobs": "Job"}))
		return true
	case "/apis/cert-manager.io/v1":
		writeJSON(w, apiResourceList("cert-manager.io/v1", map[string]string{
			"certificates": "Certificate",
			"issuers":      "Issuer",
		}))
		return true
	case "/apis/config.openshift.io/v1":
		writeJSON(w, apiResourceList("config.openshift.io/v1", map[string]string{"dnses": "DNS"}))
		return true
	case "/apis/oauth.openshift.io/v1":
		writeJSON(w, apiResourceList("oauth.openshift.io/v1", map[string]string{"oauthclients": "OAuthClient"}))
		return true
	case "/apis/networking.k8s.io/v1":
		writeJSON(w, apiResourceList("networking.k8s.io/v1", map[string]string{"networkpolicies": "NetworkPolicy"}))
		return true
	case "/apis/rbac.authorization.k8s.io/v1":
		writeJSON(w, apiResourceList("rbac.authorization.k8s.io/v1", map[string]string{
			"clusterrolebindings": "ClusterRoleBinding",
			"clusterroles":        "ClusterRole",
			"rolebindings":        "RoleBinding",
			"roles":               "Role",
		}))
		return true
	case "/apis/route.openshift.io/v1":
		writeJSON(w, apiResourceList("route.openshift.io/v1", map[string]string{"routes": "Route"}))
		return true
	case "/apis/security.openshift.io/v1":
		writeJSON(w, apiResourceList("security.openshift.io/v1", map[string]string{"securitycontextconstraints": "SecurityContextConstraints"}))
		return true
	case "/apis/multicluster.openshift.io/v1":
		writeJSON(w, apiResourceList("multicluster.openshift.io/v1", map[string]string{"multiclusterengines": "MultiClusterEngine"}))
		return true
	}
	return false
}

func isOutOfNamespaceResourcePath(path string) bool {
	const releaseNamespace = "/flightctl/"
	if strings.HasPrefix(path, "/api/v1/namespaces"+releaseNamespace) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/apis/"), "/")
	if len(parts) >= 4 && parts[2] == "namespaces" {
		return !strings.HasPrefix(path, "/apis/"+parts[0]+"/"+parts[1]+"/namespaces"+releaseNamespace)
	}
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/apis/")
}

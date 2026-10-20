package flightctl_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouteExternalCertificate(t *testing.T) {
	routes := []struct {
		name   string
		source string
	}{
		{
			name:   "CLI artifacts",
			source: "flightctl/templates/cli-artifacts/flightctl-cli-artifacts-route.yaml",
		},
		{
			name:   "UI",
			source: "flightctl/templates/ui/flightctl-ui-route.yaml",
		},
	}

	tests := []struct {
		name                 string
		routeCertificate     string
		certificateMethod    string
		certManagerInstalled bool
		externalCert         bool
		upgrade              bool
		existingObject       string
		expectedDocuments    int
	}{
		{
			name:                 "When auto mode detects cert-manager without an existing Secret it should omit externalCertificate",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			expectedDocuments:    3,
		},
		{
			name:              "When auto mode falls back to builtin certificates it should include externalCertificate",
			certificateMethod: "auto",
			externalCert:      true,
			expectedDocuments: 3,
		},
		{
			name:              "When auto mode uses builtin certificates during an upgrade it should include externalCertificate",
			certificateMethod: "auto",
			externalCert:      true,
			upgrade:           true,
			expectedDocuments: 3,
		},
		{
			name:                 "When auto mode finds an existing TLS Secret it should include externalCertificate",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			externalCert:         true,
			existingObject:       "secret",
			expectedDocuments:    3,
		},
		{
			name:                 "When auto mode finds an existing Route with externalCertificate it should preserve it",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			externalCert:         true,
			existingObject:       "route",
			expectedDocuments:    3,
		},
		{
			name:              "When routeExternalCertificate is false it should omit externalCertificate",
			certificateMethod: "cert-manager",
			routeCertificate:  "false",
			expectedDocuments: 1,
		},
		{
			name:              "When routeExternalCertificate is true it should include externalCertificate",
			certificateMethod: "auto",
			routeCertificate:  "true",
			externalCert:      true,
			expectedDocuments: 3,
		},
		{
			name:                 "When routeExternalCertificate is true with cert-manager and no Secret it should defer externalCertificate",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			routeCertificate:     "true",
			expectedDocuments:    3,
		},
		{
			name:                 "When routeExternalCertificate is true with cert-manager and an existing Secret it should include externalCertificate",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			routeCertificate:     "true",
			externalCert:         true,
			existingObject:       "secret",
			expectedDocuments:    3,
		},
	}

	for _, route := range routes {
		route := route
		t.Run(route.name, func(t *testing.T) {
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					manifest := renderChart(t, tt.routeCertificate, tt.certificateMethod, tt.certManagerInstalled, tt.upgrade, tt.existingObject)
					documents := sourceDocuments(manifest, route.source)

					require.Len(t, documents, tt.expectedDocuments)
					if tt.externalCert {
						require.Contains(t, strings.Join(documents, "\n"), "externalCertificate:")
					} else {
						require.NotContains(t, strings.Join(documents, "\n"), "externalCertificate:")
					}
				})
			}
		})
	}
}

func TestCABundleCreatorRouteCustomHostRBAC(t *testing.T) {
	manifest := renderChart(t, "auto", "auto", true, false, "")
	documents := sourceDocuments(manifest, "flightctl/templates/certs/flightctl-ca-bundle-creator-rbac.yaml")
	require.NotEmpty(t, documents)

	role := strings.Join(documents, "\n")
	require.Contains(t, role, `resources: ["routes/custom-host"]`)
	// OpenShift cert-field SAR omits resource name; resourceNames would break authz.
	customHostRuleStart := strings.Index(role, `resources: ["routes/custom-host"]`)
	require.GreaterOrEqual(t, customHostRuleStart, 0)
	customHostRule := role[customHostRuleStart:]
	if nextRule := strings.Index(customHostRule[1:], "\n  - apiGroups:"); nextRule >= 0 {
		customHostRule = customHostRule[:nextRule+1]
	}
	require.Contains(t, customHostRule, `verbs: ["create"]`)
	require.NotContains(t, customHostRule, "resourceNames:")
}

func renderChart(t *testing.T, routeCertificate, certificateMethod string, certManagerInstalled, upgrade bool, existingObject string) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to determine chart test path")

	args := []string{
		"template",
		"flightctl",
		filepath.Dir(filename),
		"--namespace",
		"flightctl",
		"--kube-version",
		"1.32.0",
		"--disable-openapi-validation",
		"--set-string",
		"global.enableOpenShiftExtensions=true",
		"--set-string",
		"global.exposeServicesMethod=route",
		"--set-string",
		"global.baseDomain=apps.example.com",
		"--set-string",
		"global.auth.type=none",
		"--set-string",
		"global.generateCertificates=" + certificateMethod,
		"--api-versions",
		"route.openshift.io/v1",
	}
	if upgrade {
		args = append(args, "--is-upgrade")
	}
	if certManagerInstalled {
		args = append(args, "--api-versions", "cert-manager.io/v1/Certificate")
	}
	if routeCertificate != "" {
		args = append(args, "--set-string", "global.routeExternalCertificate="+routeCertificate)
	}
	if existingObject != "" {
		server := httptest.NewServer(helmLookupHandler(existingObject))
		t.Cleanup(server.Close)
		args = append(args, "--dry-run=server", "--kube-apiserver", server.URL, "--kube-token", "test")
	}

	output, err := exec.Command("helm", args...).CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}

func helmLookupHandler(existingObject string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			writeJSON(w, map[string]any{"gitVersion": "v1.32.0"})
			return
		}

		switch r.URL.Path {
		case "/api":
			writeJSON(w, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
			return
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
			}})
			return
		case "/api/v1":
			writeJSON(w, apiResourceList("v1", map[string]string{
				"configmaps":             "ConfigMap",
				"persistentvolumeclaims": "PersistentVolumeClaim",
				"pods":                   "Pod",
				"secrets":                "Secret",
				"serviceaccounts":        "ServiceAccount",
				"services":               "Service",
			}))
			return
		case "/apis/apps/v1":
			writeJSON(w, apiResourceList("apps/v1", map[string]string{
				"deployments":  "Deployment",
				"statefulsets": "StatefulSet",
			}))
			return
		case "/apis/batch/v1":
			writeJSON(w, apiResourceList("batch/v1", map[string]string{"jobs": "Job"}))
			return
		case "/apis/cert-manager.io/v1":
			writeJSON(w, apiResourceList("cert-manager.io/v1", map[string]string{
				"certificates": "Certificate",
				"issuers":      "Issuer",
			}))
			return
		case "/apis/config.openshift.io/v1":
			writeJSON(w, apiResourceList("config.openshift.io/v1", map[string]string{"dnses": "DNS"}))
			return
		case "/apis/oauth.openshift.io/v1":
			writeJSON(w, apiResourceList("oauth.openshift.io/v1", map[string]string{"oauthclients": "OAuthClient"}))
			return
		case "/apis/networking.k8s.io/v1":
			writeJSON(w, apiResourceList("networking.k8s.io/v1", map[string]string{"networkpolicies": "NetworkPolicy"}))
			return
		case "/apis/rbac.authorization.k8s.io/v1":
			writeJSON(w, apiResourceList("rbac.authorization.k8s.io/v1", map[string]string{
				"clusterrolebindings": "ClusterRoleBinding",
				"clusterroles":        "ClusterRole",
				"rolebindings":        "RoleBinding",
				"roles":               "Role",
			}))
			return
		case "/apis/route.openshift.io/v1":
			writeJSON(w, apiResourceList("route.openshift.io/v1", map[string]string{"routes": "Route"}))
			return
		case "/apis/security.openshift.io/v1":
			writeJSON(w, apiResourceList("security.openshift.io/v1", map[string]string{"securitycontextconstraints": "SecurityContextConstraints"}))
			return
		}

		if existingObject == "secret" && strings.HasSuffix(r.URL.Path, "/secrets/flightctl-ui-server-tls") {
			writeJSON(w, map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata":   map[string]string{"name": "flightctl-ui-server-tls"},
			})
			return
		}
		if existingObject == "secret" && strings.HasSuffix(r.URL.Path, "/secrets/flightctl-cli-artifacts-server-tls") {
			writeJSON(w, map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata":   map[string]string{"name": "flightctl-cli-artifacts-server-tls"},
			})
			return
		}
		if existingObject == "route" && strings.HasSuffix(r.URL.Path, "/routes/flightctl-ui") {
			writeJSON(w, routeWithExternalCertificate("flightctl-ui"))
			return
		}
		if existingObject == "route" && strings.HasSuffix(r.URL.Path, "/routes/flightctl-cli-artifacts-route") {
			writeJSON(w, routeWithExternalCertificate("flightctl-cli-artifacts-route"))
			return
		}

		http.NotFound(w, r)
	})
}

func apiGroup(name string) map[string]any {
	return map[string]any{
		"name": name,
		"versions": []map[string]string{{
			"groupVersion": name + "/v1",
			"version":      "v1",
		}},
		"preferredVersion": map[string]string{
			"groupVersion": name + "/v1",
			"version":      "v1",
		},
	}
}

func apiResourceList(groupVersion string, resources map[string]string) map[string]any {
	resourceList := make([]map[string]any, 0, len(resources))
	for name, kind := range resources {
		resourceList = append(resourceList, map[string]any{
			"name":       name,
			"namespaced": true,
			"kind":       kind,
		})
	}
	return map[string]any{
		"kind":         "APIResourceList",
		"groupVersion": groupVersion,
		"resources":    resourceList,
	}
}

func routeWithExternalCertificate(name string) map[string]any {
	return map[string]any{
		"apiVersion": "route.openshift.io/v1",
		"kind":       "Route",
		"metadata": map[string]any{
			"name":        name,
			"labels":      map[string]string{"app.kubernetes.io/managed-by": "Helm"},
			"annotations": map[string]string{"meta.helm.sh/release-name": "flightctl", "meta.helm.sh/release-namespace": "flightctl"},
		},
		"spec": map[string]any{
			"tls": map[string]any{
				"externalCertificate": map[string]string{"name": name + "-tls"},
			},
		},
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func sourceDocuments(manifest, source string) []string {
	marker := "# Source: " + source
	var documents []string
	for remaining := manifest; ; {
		index := strings.Index(remaining, marker)
		if index < 0 {
			return documents
		}

		document := remaining[index:]
		if separator := strings.Index(document, "\n---"); separator >= 0 {
			document = document[:separator]
		}
		documents = append(documents, document)
		remaining = remaining[index+len(marker):]
	}
}

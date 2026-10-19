package k8s

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A run picks its cluster twice: once for the Go client and once for every
// kubectl and helm command line the providers build. These tests pin the two
// to the same answer, because the failure when they diverge is silent - the
// client reads one cluster's state while the commands change another's.
//
// No live cluster is involved: the kubeconfigs point at httptest servers, and
// the assertions are about which server a client was built against.

// stubAPIServer stands in for an API server and reports the path of the last
// request it served, which is enough to tell two clients apart.
func stubAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// writeKubeConfig writes a kubeconfig with one context per named server and
// the given current context, and returns its path.
func writeKubeConfig(t *testing.T, name string, servers map[string]string, currentContext string) string {
	t.Helper()

	var clusters, contexts []string
	for ctxName, server := range servers {
		clusters = append(clusters, fmt.Sprintf(
			"- name: %s-cluster\n  cluster:\n    server: %s", ctxName, server))
		contexts = append(contexts, fmt.Sprintf(
			"- name: %s\n  context:\n    cluster: %s-cluster\n    user: %s-user", ctxName, ctxName, ctxName))
	}
	var users []string
	for ctxName := range servers {
		users = append(users, fmt.Sprintf("- name: %s-user\n  user: {}", ctxName))
	}

	body := strings.Join([]string{
		"apiVersion: v1",
		"kind: Config",
		"current-context: " + currentContext,
		"clusters:",
		strings.Join(clusters, "\n"),
		"contexts:",
		strings.Join(contexts, "\n"),
		"users:",
		strings.Join(users, "\n"),
		"",
	}, "\n")

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestResolveKubeConfigPrefersTheConfiguredPath(t *testing.T) {
	envPath := writeKubeConfig(t, "env.yaml", map[string]string{"env-ctx": "https://env.invalid"}, "env-ctx")
	explicitPath := writeKubeConfig(t, "explicit.yaml", map[string]string{"explicit-ctx": "https://explicit.invalid"}, "explicit-ctx")

	tests := []struct {
		name           string
		kubeconfigEnv  string
		path           string
		kubeContext    string
		wantPath       string
		wantContext    string
		wantResolveErr bool
	}{
		{
			name:          "When a path is configured, it should win over KUBECONFIG",
			kubeconfigEnv: envPath,
			path:          explicitPath,
			wantPath:      explicitPath,
		},
		{
			name:          "When no path is configured, it should fall back to KUBECONFIG",
			kubeconfigEnv: envPath,
			wantPath:      envPath,
		},
		{
			name:          "When the configured path is blank, it should fall back to KUBECONFIG",
			kubeconfigEnv: envPath,
			path:          "   ",
			wantPath:      envPath,
		},
		{
			name:          "When KUBECONFIG lists several files, it should narrow to one that exists",
			kubeconfigEnv: filepath.Join(t.TempDir(), "missing.yaml") + string(os.PathListSeparator) + envPath,
			wantPath:      envPath,
		},
		{
			name:          "When a context is configured, it should be returned alongside the path",
			kubeconfigEnv: envPath,
			kubeContext:   "explicit-ctx",
			wantPath:      envPath,
			wantContext:   "explicit-ctx",
		},
		{
			name:          "When no context is configured, it should leave the choice to the file",
			kubeconfigEnv: envPath,
			kubeContext:   "  ",
			wantPath:      envPath,
			wantContext:   "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", test.kubeconfigEnv)

			path, kubeContext, err := ResolveKubeConfig(test.path, test.kubeContext)
			if test.wantResolveErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantPath, path)
			require.Equal(t, test.wantContext, kubeContext)
		})
	}
}

func TestResolveKubeConfigReportsNoKubeConfig(t *testing.T) {
	// No KUBECONFIG and a home with no ~/.kube/config: the run has nothing to
	// talk to, and must say so rather than hand back an empty path.
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", t.TempDir())

	_, _, err := ResolveKubeConfig("", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "kubeconfig")
}

func TestNewClientForKubeConfigHonoursTheContext(t *testing.T) {
	alpha := stubAPIServer(t)
	beta := stubAPIServer(t)
	path := writeKubeConfig(t, "two-contexts.yaml", map[string]string{
		"alpha": alpha.URL,
		"beta":  beta.URL,
	}, "alpha")

	tests := []struct {
		name        string
		kubeContext string
		wantHost    string
	}{
		{
			name:     "When no context is given, it should use the file's current context",
			wantHost: alpha.URL,
		},
		{
			name:        "When a context is given, it should override the file's current context",
			kubeContext: "beta",
			wantHost:    beta.URL,
		},
		{
			name:        "When the current context is named explicitly, it should still be used",
			kubeContext: "alpha",
			wantHost:    alpha.URL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Set so a leaked environment lookup would pick the wrong file
			// and fail the assertion rather than pass by accident.
			t.Setenv("KUBECONFIG", writeKubeConfig(t, "decoy.yaml",
				map[string]string{"decoy": "https://decoy.invalid"}, "decoy"))

			_, restConfig, err := NewClientForKubeConfig(path, test.kubeContext)
			require.NoError(t, err)
			require.Equal(t, test.wantHost, restConfig.Host)
		})
	}
}

func TestNewClientForKubeConfigReadsTheGivenFile(t *testing.T) {
	// Two files, each with a context of the same name pointing elsewhere: the
	// path argument has to decide, not the environment.
	alpha := stubAPIServer(t)
	beta := stubAPIServer(t)
	alphaPath := writeKubeConfig(t, "alpha.yaml", map[string]string{"shared": alpha.URL}, "shared")
	betaPath := writeKubeConfig(t, "beta.yaml", map[string]string{"shared": beta.URL}, "shared")

	t.Setenv("KUBECONFIG", alphaPath)
	_, restConfig, err := NewClientForKubeConfig(betaPath, "")
	require.NoError(t, err)
	require.Equal(t, beta.URL, restConfig.Host, "the explicit path must win over KUBECONFIG")

	t.Setenv("KUBECONFIG", betaPath)
	_, restConfig, err = NewClientForKubeConfig(alphaPath, "")
	require.NoError(t, err)
	require.Equal(t, alpha.URL, restConfig.Host)
}

func TestNewClientForKubeConfigRejectsUnusableInput(t *testing.T) {
	path := writeKubeConfig(t, "single.yaml", map[string]string{"only": "https://only.invalid"}, "only")

	tests := []struct {
		name        string
		path        string
		kubeContext string
	}{
		{name: "When the path is empty, it should fail", path: ""},
		{name: "When the path is blank, it should fail", path: "  "},
		{name: "When the file does not exist, it should fail", path: filepath.Join(t.TempDir(), "absent.yaml")},
		{name: "When the context is not in the file, it should fail", path: path, kubeContext: "no-such-context"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", path)
			_, _, err := NewClientForKubeConfig(test.path, test.kubeContext)
			require.Error(t, err)
		})
	}
}

func TestNewClientAndConfigStillResolvesByDefault(t *testing.T) {
	// The legacy entrypoints take no arguments and must keep working off the
	// environment alone, because the harness and several providers call them.
	alpha := stubAPIServer(t)
	path := writeKubeConfig(t, "default.yaml", map[string]string{"alpha": alpha.URL}, "alpha")
	t.Setenv("KUBECONFIG", path)

	clientset, restConfig, err := NewClientAndConfig()
	require.NoError(t, err)
	require.NotNil(t, clientset)
	require.Equal(t, alpha.URL, restConfig.Host)

	client, err := NewClient()
	require.NoError(t, err)
	require.NotNil(t, client)
}

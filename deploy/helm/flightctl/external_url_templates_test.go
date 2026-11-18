package flightctl_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type externalURLs struct {
	baseURL, agentEndpointURL, uiURL, apiExternalURL, cliArtifactsURL     string
	apiSANs, remoteAccessSANs, imageBuilderSANs, uiSANs, cliArtifactsSANs []string
}

func TestExternalURLs(t *testing.T) {
	defaults := externalURLs{
		baseURL:          "https://api.example.test/",
		agentEndpointURL: "https://agent-api.example.test/",
		uiURL:            "http://ui.example.test",
		apiExternalURL:   "https://api.example.test",
		cliArtifactsURL:  "http://cli-artifacts.example.test",
		apiSANs:          []string{"api.example.test", "agent-api.example.test", "flightctl-api", "flightctl-api.public", "flightctl-api.public.svc.cluster.local"},
		remoteAccessSANs: []string{"remote-access.example.test", "agent-remote-access.example.test", "flightctl-remote-access", "flightctl-remote-access-agent", "flightctl-remote-access.public", "flightctl-remote-access-agent.public", "flightctl-remote-access.public.svc.cluster.local", "flightctl-remote-access-agent.public.svc.cluster.local"},
		imageBuilderSANs: []string{"imagebuilder-api.example.test", "flightctl-imagebuilder-api", "flightctl-imagebuilder-api.public", "flightctl-imagebuilder-api.public.svc.cluster.local"},
		uiSANs:           []string{"ui.example.test", "flightctl-ui", "flightctl-ui.public", "flightctl-ui.public.svc.cluster.local"},
		cliArtifactsSANs: []string{"cli-artifacts.example.test", "flightctl-cli-artifacts", "flightctl-cli-artifacts.public", "flightctl-cli-artifacts.public.svc.cluster.local"},
	}
	insert := func(sans []string, after int, extra ...string) []string {
		return append(append(append([]string{}, sans[:after]...), extra...), sans[after:]...)
	}
	singleHost := []string{
		"--set-string", "global.uiUrl=https://edge.example.com",
		"--set-string", "global.apiUrl=https://edge.example.com/_/flightctl",
		"--set-string", "global.agentApiUrl=https://edge.example.com:7443",
		"--set-string", "global.cliArtifactsUrl=https://edge.example.com/_/cli-artifacts",
	}
	singleHostURLs := externalURLs{
		baseURL:          "https://edge.example.com/_/flightctl/",
		agentEndpointURL: "https://edge.example.com:7443/",
		uiURL:            "https://edge.example.com",
		apiExternalURL:   "https://edge.example.com/_/flightctl",
		cliArtifactsURL:  "https://edge.example.com/_/cli-artifacts",
		apiSANs:          insert(defaults.apiSANs, 2, "edge.example.com"),
		remoteAccessSANs: insert(defaults.remoteAccessSANs, 2, "edge.example.com"),
		imageBuilderSANs: insert(defaults.imageBuilderSANs, 1, "edge.example.com"),
		uiSANs:           insert(defaults.uiSANs, 1, "edge.example.com"),
		cliArtifactsSANs: insert(defaults.cliArtifactsSANs, 1, "edge.example.com"),
	}
	withAPI := func(base externalURLs, apiURL string, apiSANs, remoteAccessSANs, imageBuilderSANs []string) externalURLs {
		base.baseURL, base.apiExternalURL = apiURL+"/", apiURL
		base.apiSANs, base.remoteAccessSANs, base.imageBuilderSANs = apiSANs, remoteAccessSANs, imageBuilderSANs
		return base
	}

	for _, tc := range []struct {
		name     string
		args     []string
		expected externalURLs
	}{
		{
			name:     "When no URL is set it should derive the URLs from baseDomain",
			expected: defaults,
		},
		{
			name:     "When the services share one host it should use the URLs of the Podman layout",
			args:     singleHost,
			expected: singleHostURLs,
		},
		{
			name: "When services are exposed on node ports it should derive the node port URLs",
			args: []string{"-f", "values.nodeport.yaml"},
			expected: func() externalURLs {
				urls := defaults
				urls.baseURL, urls.apiExternalURL = "https://api.example.test:3443/", "https://example.test:3443"
				urls.agentEndpointURL = "https://agent-api.example.test:7443/"
				urls.uiURL, urls.cliArtifactsURL = "http://example.test:9001", "http://example.test:8090"
				return urls
			}(),
		},
		{
			name: "When the gateway uses a non-default port it should derive the URLs with that port",
			args: []string{"--set-string", "global.exposeServicesMethod=gateway", "--set", "global.gateway.ports.tls=8443"},
			expected: func() externalURLs {
				urls := defaults
				urls.baseURL, urls.apiExternalURL = "https://api.example.test:8443/", "https://api.example.test:8443"
				urls.agentEndpointURL = "https://agent-api.example.test:8443/"
				return urls
			}(),
		},
		{
			name:     "When services are exposed on node ports it should use the overrides",
			args:     append([]string{"-f", "values.nodeport.yaml"}, singleHost...),
			expected: singleHostURLs,
		},
		{
			name:     "When the gateway uses a non-default port it should use the overrides",
			args:     append([]string{"--set-string", "global.exposeServicesMethod=gateway", "--set", "global.gateway.ports.tls=8443"}, singleHost...),
			expected: singleHostURLs,
		},
		{
			name: "When users and agents use their own hosts it should add the hosts the CLI and agents derive",
			args: []string{
				"--set-string", "global.apiUrl=https://api.users.example.com",
				"--set-string", "global.agentApiUrl=https://agent-api.devices.example.com",
			},
			expected: func() externalURLs {
				urls := withAPI(defaults, "https://api.users.example.com",
					insert(defaults.apiSANs, 2, "api.users.example.com", "agent-api.devices.example.com"),
					insert(defaults.remoteAccessSANs, 2, "remote-access.users.example.com", "agent-remote-access.devices.example.com"),
					insert(defaults.imageBuilderSANs, 1, "imagebuilder-api.users.example.com"))
				urls.agentEndpointURL = "https://agent-api.devices.example.com/"
				return urls
			}(),
		},
		{
			name: "When the API host uses a non-default port it should keep the host for the services the CLI derives",
			args: []string{"--set-string", "global.apiUrl=https://api.users.example.com:3443"},
			expected: withAPI(defaults, "https://api.users.example.com:3443",
				insert(defaults.apiSANs, 2, "api.users.example.com"),
				insert(defaults.remoteAccessSANs, 2, "api.users.example.com"),
				insert(defaults.imageBuilderSANs, 1, "api.users.example.com")),
		},
		{
			name: "When an override matches a derived host it should not duplicate certificate names",
			args: []string{
				"--set-string", "global.uiUrl=http://ui.example.test",
				"--set-string", "global.apiUrl=https://api.example.test",
				"--set-string", "global.agentApiUrl=https://agent-api.example.test",
				"--set-string", "global.cliArtifactsUrl=http://cli-artifacts.example.test",
			},
			expected: defaults,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := externalURLChart(t, tc.args...)

			apiService := runtimeConfig(t, docs, "ConfigMap/flightctl-api-config")["service"].(map[string]any)
			require.Equal(t, tc.expected.baseURL, apiService["baseUrl"])
			require.Equal(t, tc.expected.agentEndpointURL, apiService["baseAgentEndpointUrl"])
			require.Equal(t, tc.expected.uiURL, apiService["baseUIUrl"])
			require.Equal(t, toAny(tc.expected.apiSANs), apiService["altNames"])

			workerService := runtimeConfig(t, docs, "ConfigMap/flightctl-imagebuilder-worker-config")["service"].(map[string]any)
			require.Equal(t, tc.expected.agentEndpointURL, workerService["baseAgentEndpointUrl"])
			require.Equal(t, tc.expected.uiURL, workerService["baseUIUrl"])

			uiConfig := docs["ConfigMap/flightctl-ui"]["data"].(map[string]any)
			require.Equal(t, tc.expected.uiURL, uiConfig["BASE_UI_URL"])
			require.Equal(t, tc.expected.apiExternalURL, uiConfig["FLIGHTCTL_SERVER_EXTERNAL"])

			cliArtifacts := podSpec(docs["Deployment/flightctl-cli-artifacts"])
			env := namedEntry(namedEntry(cliArtifacts["containers"], "flightctl-cli-artifacts")["env"], "CLI_ARTIFACTS_BASE_URL")
			require.NotNil(t, env)
			require.Equal(t, tc.expected.cliArtifactsURL, env["value"])

			generator := namedEntry(podSpec(docs["Job/flightctl-cert-generator-1"])["containers"], "cert-generator")
			require.NotNil(t, generator)
			command := generator["command"]
			require.Equal(t, tc.expected.apiSANs, flagValues(command, "--api-san"))
			require.Equal(t, tc.expected.remoteAccessSANs, flagValues(command, "--remote-access-san"))
			require.Equal(t, tc.expected.imageBuilderSANs, flagValues(command, "--imagebuilder-api-san"))
			require.Equal(t, tc.expected.uiSANs, flagValues(command, "--ui-san"))
			require.Equal(t, tc.expected.cliArtifactsSANs, flagValues(command, "--cli-artifacts-san"))
		})
	}
}

func TestExternalURLsCertManager(t *testing.T) {
	output, err := catalogChart(t, "global.generateCertificates=cert-manager", "global.apiUrl=https://edge.example.com/_/flightctl", "global.agentApiUrl=https://agent-api.devices.example.com")
	require.NoError(t, err, output)
	docs := catalogDocuments(t, output)
	for name, hosts := range map[string][]string{
		"Certificate/flightctl-api-server-tls":           {"edge.example.com", "agent-api.devices.example.com"},
		"Certificate/flightctl-remote-access-server-tls": {"edge.example.com", "agent-remote-access.devices.example.com"},
	} {
		certificate := docs[name]
		require.NotNil(t, certificate, name)
		dnsNames := certificate["spec"].(map[string]any)["dnsNames"]
		for _, host := range hosts {
			require.Contains(t, dnsNames, host, name)
		}
	}
}

func TestExternalURLsInvalidValues(t *testing.T) {
	for _, setting := range []string{
		"global.uiUrl=edge.example.com",
		"global.uiUrl=https://user:secret@edge.example.com",
		"global.uiUrl=https://edge.example.com/",
		"global.apiUrl=http://edge.example.com",
		"global.apiUrl=https://edge.example.com/_/flightctl?x=1",
		"global.apiUrl=https://edge.example.com/_/flightctl/",
		"global.agentApiUrl=https://edge.example.com/agent",
		"global.agentApiUrl=https://edge.example.com:7443/",
		"global.cliArtifactsUrl=https://edge.example.com/_/cli-artifacts#top",
		"global.cliArtifactsUrl=https://edge.example.com/_/cli-artifacts/",
	} {
		t.Run("When "+setting+" it should reject", func(t *testing.T) {
			output, err := catalogChart(t, setting)
			require.Error(t, err, output)
			key, _, _ := strings.Cut(strings.TrimPrefix(setting, "global."), "=")
			require.Contains(t, output, "/global/"+key)
		})
	}
}

func externalURLChart(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	args = append([]string{"template", "flightctl", ".", "--namespace", "public", "--set-string", "global.enableOpenShiftExtensions=false", "--set-string", "global.exposeServicesMethod=none", "--set-string", "global.auth.type=none", "--set-string", "global.generateCertificates=builtin", "--set-string", "global.baseDomain=example.test"}, args...)
	command := exec.Command("helm", args...)
	command.Dir = filepath.Dir(file)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return catalogDocuments(t, string(output))
}

func runtimeConfig(t *testing.T, docs map[string]map[string]any, name string) map[string]any {
	t.Helper()
	configMap := docs[name]
	require.NotNil(t, configMap, name)
	var config map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(configMap["data"].(map[string]any)["config.yaml"].(string)), &config))
	return config
}

func podSpec(workload map[string]any) map[string]any {
	return workload["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}

func flagValues(command any, flag string) []string {
	values, _ := command.([]any)
	var result []string
	for i := 0; i+1 < len(values); i++ {
		if values[i] == flag {
			result = append(result, values[i+1].(string))
		}
	}
	return result
}

func toAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

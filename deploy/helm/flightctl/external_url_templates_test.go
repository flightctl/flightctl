package flightctl_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// schemaFieldPath normalizes a Helm schema validation field reference from
// either JSON-pointer format (/global/uiUrl) or dot notation (global.uiUrl)
// into canonical dot notation.
func schemaFieldPath(ref string) string {
	return strings.TrimPrefix(strings.ReplaceAll(ref, "/", "."), ".")
}

func TestSchemaFieldPathNormalization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected string
	}{
		{"When given dot notation it should return it unchanged", "global.uiUrl", "global.uiUrl"},
		{"When given a JSON-pointer it should convert to dot notation", "/global/uiUrl", "global.uiUrl"},
		{"When given a deeper JSON-pointer it should convert each segment", "/a/b/c", "a.b.c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, schemaFieldPath(tc.input))
		})
	}

	// Verify that targeted normalization of a specific field reference
	// works for both diagnostic formats and preserves other fields.
	jsonPointerDiag := "- /global/uiUrl: Does not match pattern\n"
	dotPathDiag := "- global.uiUrl: Does not match pattern\n"
	for _, diag := range []string{jsonPointerDiag, dotPathDiag} {
		normalized := strings.ReplaceAll(diag, "/global/uiUrl", "global.uiUrl")
		require.Contains(t, normalized, "global.uiUrl",
			"after normalization the expected dot-path field should appear")
	}
	// A different field (apiUrl) should not be affected by uiUrl
	// normalization.
	otherField := "- /global/apiUrl: Does not match pattern\n"
	normalized := strings.ReplaceAll(otherField, "/global/uiUrl", "global.uiUrl")
	require.NotContains(t, normalized, "global.uiUrl",
		"normalizing uiUrl should not affect apiUrl references")
	require.Contains(t, normalized, "/global/apiUrl",
		"apiUrl reference should be preserved verbatim")
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
		// IPv6 literal addresses are not supported.
		"global.uiUrl=https://[::1]",
		"global.apiUrl=https://[2001:db8::10]/_/flightctl",
		"global.agentApiUrl=https://[2001:db8::10]:7443",
		"global.cliArtifactsUrl=https://[::ffff:192.168.1.1]/_/cli",
	} {
		t.Run("When "+setting+" it should reject", func(t *testing.T) {
			output, err := catalogChart(t, setting)
			require.Error(t, err, output)
			key, _, _ := strings.Cut(strings.TrimPrefix(setting, "global."), "=")
			// Normalize any JSON-pointer field paths in the Helm error
			// output to dot notation so a single assertion works
			// regardless of Helm version (3.17+ uses dot-path, older
			// versions use JSON-pointer).
			expected := schemaFieldPath("global." + key)
			normalized := strings.ReplaceAll(output, "/global/"+key, expected)
			require.Contains(t, normalized, expected,
				"expected schema error to reference field %q", expected)
		})
	}
}

func TestExternalURLsBracketsInPath(t *testing.T) {
	// The IPv6 authority exclusion must not reject brackets that appear
	// in path segments.  The schema's path character class does not
	// exclude them, so this should still be accepted.
	output, err := catalogChart(t, "global.uiUrl=https://edge.example.com/path[1]")
	require.NoError(t, err, output)
}

// certManagerChart renders the chart with cert-manager as the certificate
// generation method. extraArgs are passed directly to helm (e.g.
// --api-versions).
func certManagerChart(t *testing.T, extraArgs []string, settings ...string) (string, error) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	args := []string{"template", "flightctl", filepath.Dir(file),
		"--namespace", "public",
		"--set-string", "global.enableOpenShiftExtensions=false",
		"--set-string", "global.exposeServicesMethod=none",
		"--set-string", "global.auth.type=none",
		"--set-string", "global.generateCertificates=cert-manager",
		"--set-string", "global.baseDomain=example.test",
		"--kube-version", "1.32.0",
	}
	args = append(args, extraArgs...)
	for _, setting := range settings {
		args = append(args, "--set-string", setting)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
	return string(output), err
}

func TestCertManagerRejectsIPv4Hosts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		key     string // expected key name in error message
	}{
		{
			name:    "When uiUrl uses an IPv4 host with cert-manager it should reject",
			setting: "global.uiUrl=https://192.168.1.1",
			key:     "global.uiUrl",
		},
		{
			name:    "When apiUrl uses an IPv4 host with cert-manager it should reject",
			setting: "global.apiUrl=https://10.0.0.1/_/flightctl",
			key:     "global.apiUrl",
		},
		{
			name:    "When agentApiUrl uses an IPv4 host with port and cert-manager it should reject",
			setting: "global.agentApiUrl=https://172.16.0.1:7443",
			key:     "global.agentApiUrl",
		},
		{
			name:    "When cliArtifactsUrl uses an IPv4 host with path and cert-manager it should reject",
			setting: "global.cliArtifactsUrl=https://10.0.0.1/_/cli-artifacts",
			key:     "global.cliArtifactsUrl",
		},
		{
			name:    "When uiUrl uses a loopback IPv4 address with cert-manager it should reject",
			setting: "global.uiUrl=https://127.0.0.1",
			key:     "global.uiUrl",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t, nil, tc.setting)
			require.Error(t, err, output)
			require.Contains(t, output, tc.key,
				"error should name the offending setting")
			require.Contains(t, output, "IPv4",
				"error should mention IPv4")
			require.Contains(t, output, "hostname",
				"error should mention hostname requirement")
		})
	}
}

func TestCertManagerAutoDetectedRejectsIPv4(t *testing.T) {
	// When generateCertificates=auto and cert-manager.io/v1/Certificate is
	// available, the resolved method is cert-manager and IPv4 hosts must be
	// rejected for all four override keys.
	for _, tc := range []struct {
		name    string
		setting string
		key     string
	}{
		{
			name:    "When auto detects cert-manager and uiUrl is IPv4 it should reject",
			setting: "global.uiUrl=https://192.168.1.1",
			key:     "global.uiUrl",
		},
		{
			name:    "When auto detects cert-manager and apiUrl is IPv4 it should reject",
			setting: "global.apiUrl=https://10.0.0.1/_/flightctl",
			key:     "global.apiUrl",
		},
		{
			name:    "When auto detects cert-manager and agentApiUrl is IPv4 it should reject",
			setting: "global.agentApiUrl=https://172.16.0.1:7443",
			key:     "global.agentApiUrl",
		},
		{
			name:    "When auto detects cert-manager and cliArtifactsUrl is IPv4 it should reject",
			setting: "global.cliArtifactsUrl=https://10.0.0.1/_/cli-artifacts",
			key:     "global.cliArtifactsUrl",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t,
				[]string{"--api-versions", "cert-manager.io/v1/Certificate"},
				"global.generateCertificates=auto", tc.setting)
			require.Error(t, err, output)
			require.Contains(t, output, tc.key)
			require.Contains(t, output, "IPv4")
		})
	}
}

func TestBuiltinAcceptsIPv4Hosts(t *testing.T) {
	// The builtin certificate generator supports IP SANs, so IPv4 literal
	// hosts must be accepted.
	for _, tc := range []struct {
		name    string
		setting string
	}{
		{
			name:    "When uiUrl uses IPv4 with builtin certs it should accept",
			setting: "global.uiUrl=https://192.168.1.1",
		},
		{
			name:    "When apiUrl uses IPv4 with builtin certs it should accept",
			setting: "global.apiUrl=https://10.0.0.1/_/flightctl",
		},
		{
			name:    "When agentApiUrl uses IPv4 with port and builtin certs it should accept",
			setting: "global.agentApiUrl=https://172.16.0.1:7443",
		},
		{
			name:    "When cliArtifactsUrl uses IPv4 with builtin certs it should accept",
			setting: "global.cliArtifactsUrl=https://10.0.0.1/_/cli-artifacts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t, nil,
				"global.generateCertificates=builtin", tc.setting)
			require.NoError(t, err, output)
		})
	}
}

func TestAutoBuiltinAcceptsIPv4Hosts(t *testing.T) {
	// When auto resolves to builtin (no cert-manager API available),
	// IPv4 hosts should be accepted. No --api-versions flag means the
	// cert-manager CRD is absent, so auto resolves to builtin.
	output, err := certManagerChart(t, nil,
		"global.generateCertificates=auto",
		"global.apiUrl=https://192.168.1.1/_/flightctl",
		"global.uiUrl=https://10.0.0.1")
	require.NoError(t, err, output)
}

func TestNoCertGenerationAcceptsIPv4Hosts(t *testing.T) {
	// When certificate generation is disabled (none), IPv4 hosts should
	// be accepted because no certificates are generated.
	output, err := certManagerChart(t, nil,
		"global.generateCertificates=none",
		"global.apiUrl=https://192.168.1.1/_/flightctl")
	require.NoError(t, err, output)
}

func TestExplicitMethodIgnoresCertManagerCapability(t *testing.T) {
	// When generateCertificates is explicitly set to builtin or none, the
	// presence of the cert-manager.io/v1/Certificate API must NOT trigger
	// IPv4 rejection — only the resolved method matters.
	certManagerAPI := []string{"--api-versions", "cert-manager.io/v1/Certificate"}
	for _, tc := range []struct {
		name    string
		method  string
		setting string
	}{
		{
			name:    "When builtin is explicit with cert-manager API and uiUrl is IPv4 it should accept",
			method:  "builtin",
			setting: "global.uiUrl=https://192.168.1.1",
		},
		{
			name:    "When builtin is explicit with cert-manager API and apiUrl is IPv4 it should accept",
			method:  "builtin",
			setting: "global.apiUrl=https://10.0.0.1/_/flightctl",
		},
		{
			name:    "When builtin is explicit with cert-manager API and agentApiUrl is IPv4 it should accept",
			method:  "builtin",
			setting: "global.agentApiUrl=https://172.16.0.1:7443",
		},
		{
			name:    "When builtin is explicit with cert-manager API and cliArtifactsUrl is IPv4 it should accept",
			method:  "builtin",
			setting: "global.cliArtifactsUrl=https://10.0.0.1/_/cli-artifacts",
		},
		{
			name:    "When none is explicit with cert-manager API and apiUrl is IPv4 it should accept",
			method:  "none",
			setting: "global.apiUrl=https://192.168.1.1/_/flightctl",
		},
		{
			name:    "When none is explicit with cert-manager API and uiUrl is IPv4 it should accept",
			method:  "none",
			setting: "global.uiUrl=https://10.0.0.1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t, certManagerAPI,
				"global.generateCertificates="+tc.method, tc.setting)
			require.NoError(t, err, output)
		})
	}
}

func TestCertManagerAcceptsHostnames(t *testing.T) {
	// Valid hostnames must still be accepted with cert-manager, including
	// hostnames that contain numeric labels.
	for _, tc := range []struct {
		name    string
		setting string
	}{
		{
			name:    "When uiUrl uses a hostname with cert-manager it should accept",
			setting: "global.uiUrl=https://edge.example.com",
		},
		{
			name:    "When apiUrl uses a hostname with cert-manager it should accept",
			setting: "global.apiUrl=https://api.example.com/_/flightctl",
		},
		{
			name:    "When agentApiUrl uses a hostname with port and cert-manager it should accept",
			setting: "global.agentApiUrl=https://agent.example.com:7443",
		},
		{
			name:    "When a hostname has a numeric prefix label with cert-manager it should accept",
			setting: "global.uiUrl=https://10host.example.com",
		},
		{
			name:    "When a hostname has a numeric-only label with cert-manager it should accept",
			setting: "global.uiUrl=https://1.example.com",
		},
		{
			name:    "When a hostname resembles an IP prefix with cert-manager it should accept",
			setting: "global.uiUrl=https://192.0.2.1.example.test",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t, nil, tc.setting)
			require.NoError(t, err, output)
		})
	}
}

func TestCertManagerNoOverridesAccepted(t *testing.T) {
	// No URL overrides set: default baseDomain-derived hostnames should
	// render without error.
	output, err := certManagerChart(t, nil)
	require.NoError(t, err, output)
}

func TestCertManagerIPv4RejectedWithDisabledConsumers(t *testing.T) {
	// Even when the UI and CLI-artifacts components are disabled, the
	// guard must still reject IPv4 hosts because the override values
	// influence other rendered resources.
	for _, tc := range []struct {
		name    string
		setting string
		key     string
	}{
		{
			name:    "When UI is disabled and uiUrl is IPv4 with cert-manager it should still reject",
			setting: "global.uiUrl=https://192.168.1.1",
			key:     "global.uiUrl",
		},
		{
			name:    "When cliArtifacts is disabled and cliArtifactsUrl is IPv4 with cert-manager it should still reject",
			setting: "global.cliArtifactsUrl=https://10.0.0.1/_/cli-artifacts",
			key:     "global.cliArtifactsUrl",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := certManagerChart(t,
				[]string{"--set", "ui.enabled=false", "--set", "cliArtifacts.enabled=false"},
				tc.setting)
			require.Error(t, err, output)
			require.Contains(t, output, tc.key)
			require.Contains(t, output, "IPv4")
		})
	}
}

func TestCertManagerAcceptsIPInPath(t *testing.T) {
	// An IPv4 address appearing in the URL path (not the host) must not be
	// rejected — only the authority is checked.
	output, err := certManagerChart(t, nil,
		"global.apiUrl=https://api.example.com/_/10.0.0.1/flightctl")
	require.NoError(t, err, output)
}

func externalURLChart(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	args = append([]string{"template", "flightctl", ".", "--namespace", "public", "--set-string", "global.enableOpenShiftExtensions=false", "--set-string", "global.exposeServicesMethod=none", "--set-string", "global.auth.type=none", "--set-string", "global.generateCertificates=builtin", "--set-string", "global.baseDomain=example.test"}, args...)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "helm", args...)
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

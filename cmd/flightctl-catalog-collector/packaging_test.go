package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// The example configurations shipped by the RPM and by the Helm chart are
// the first thing an administrator copies, so a typo in one of them is a
// user-visible packaging bug. Nothing else in the build parses them: the RPM
// installs them verbatim and Helm embeds the chart ones as an opaque string.
// These tests close that gap by running the shipped files through the same
// loader and the same component factories the collector itself uses.

const (
	// quadletConfigMountPath is where the Quadlet unit mounts
	// /etc/flightctl/flightctl-catalog-collector on the host. Every file path
	// inside a shipped configuration must be below it, because the host path
	// does not exist inside the container.
	quadletConfigMountPath = "/etc/flightctl/catalog-collector"

	// quadletHostConfigDir is the host-side directory. Seeing it inside a
	// configuration means host and container paths were confused.
	quadletHostConfigDir = "/etc/flightctl/flightctl-catalog-collector"

	// saTokenDir is where the kubelet projects the pod's own ServiceAccount
	// token when automountServiceAccountToken is enabled.
	saTokenDir = "/var/run/secrets/kubernetes.io/serviceaccount"

	// saTokenFile is the only file in that directory a shipped configuration
	// may read, and only when the values file that ships it also enables
	// automountServiceAccountToken. Without that opt-in the kubelet projects
	// nothing, so the path is as absent inside the container as any other
	// unmounted one.
	saTokenFile = saTokenDir + "/token"

	// saTokenField is the configuration key the automounted token may be
	// named in. The other path fields name files the deployment mounts
	// itself, so none of them has a reason to point at the kubelet volume.
	saTokenField = "tokenFile"
)

// placeholderRE matches the <angle-bracket> placeholders the examples use for
// site-specific values. They are not valid hostnames, so they are replaced
// before the configuration is decoded.
var placeholderRE = regexp.MustCompile(`<[a-zA-Z0-9_-]+>`)

// filePathFields are the configuration keys whose values are paths inside the
// container.
var filePathFields = []string{
	"tokenFile",
	"clientIdFile",
	"clientSecretFile",
	"certificateAuthority",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "could not find repository root from %s", dir)
		dir = parent
	}
}

// decodeAllComponents decodes every component in cfg through the factory that
// owns its type, which is what catches a misspelled or unsupported field: the
// decoder is strict and every component config that implements Validator is
// validated.
func decodeAllComponents(t *testing.T, cfg *config.Config) {
	t.Helper()

	factories := components()

	sources := map[catalogcollector.ComponentType]catalogcollector.SourceFactory{}
	for _, f := range factories.Sources {
		sources[f.Type()] = f
	}
	processors := map[catalogcollector.ComponentType]catalogcollector.ProcessorFactory{}
	for _, f := range factories.Processors {
		processors[f.Type()] = f
	}
	destinations := map[catalogcollector.ComponentType]catalogcollector.DestinationFactory{}
	for _, f := range factories.Destinations {
		destinations[f.Type()] = f
	}
	extensions := map[catalogcollector.ComponentType]catalogcollector.ExtensionFactory{}
	for _, f := range factories.Extensions {
		extensions[f.Type()] = f
	}

	decode := func(kind string, comps map[string]config.ComponentConfig, lookup func(catalogcollector.ComponentType) (catalogcollector.Factory, bool)) {
		for id, comp := range comps {
			factory, ok := lookup(comp.ID.Type)
			require.Truef(t, ok, "%s %q: no factory registered for type %q", kind, id, comp.ID.Type)
			defaults := factory.CreateDefaultConfig()
			if defaults == nil {
				continue
			}
			require.NoErrorf(t,
				config.DecodeComponent(comp.Config, defaults, kind+"."+id),
				"%s %q failed to decode", kind, id)
		}
	}

	decode("sources", cfg.Sources, func(ct catalogcollector.ComponentType) (catalogcollector.Factory, bool) {
		f, ok := sources[ct]
		return f, ok
	})
	decode("processors", cfg.Processors, func(ct catalogcollector.ComponentType) (catalogcollector.Factory, bool) {
		f, ok := processors[ct]
		return f, ok
	})
	decode("destinations", cfg.Destinations, func(ct catalogcollector.ComponentType) (catalogcollector.Factory, bool) {
		f, ok := destinations[ct]
		return f, ok
	})
	decode("extensions", cfg.Extensions, func(ct catalogcollector.ComponentType) (catalogcollector.Factory, bool) {
		f, ok := extensions[ct]
		return f, ok
	})
}

// checkContainerPaths returns an error when a shipped configuration
// references a path that will not exist inside the container.
//
// Everything must live under the mounted configuration directory. The single
// exception is the automounted ServiceAccount token, and it only exists when
// the deployment asked for it, so automountServiceAccountToken has to be
// passed in rather than assumed: a Quadlet unit has no kubelet to project it,
// and a chart values file that leaves the default alone has opted out.
func checkContainerPaths(name string, comps map[string]config.ComponentConfig, automountServiceAccountToken bool) error {
	for id, comp := range comps {
		if comp.Config == nil {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(comp.Config, &body); err != nil {
			return fmt.Errorf("%s: %s: %w", name, id, err)
		}

		for _, field := range filePathFields {
			raw, ok := body[field]
			if !ok {
				continue
			}
			value, ok := raw.(string)
			if !ok || value == "" {
				continue
			}
			if strings.HasPrefix(value, quadletHostConfigDir) {
				return fmt.Errorf(
					"%s: %s.%s is the host path %q; configurations are read inside the container, "+
						"so it must start with %q",
					name, id, field, value, quadletConfigMountPath)
			}
			if strings.HasPrefix(value, quadletConfigMountPath+"/") {
				continue
			}
			if field == saTokenField && value == saTokenFile && automountServiceAccountToken {
				continue
			}
			return fmt.Errorf(
				"%s: %s.%s is %q; it must be under the mounted configuration directory %q. "+
					"Only %s may be %q, and only where serviceAccount.automountServiceAccountToken "+
					"is enabled, because nothing projects that file otherwise",
				name, id, field, value, quadletConfigMountPath, saTokenField, saTokenFile)
		}
	}
	return nil
}

// assertContainerPaths fails when a shipped configuration references a path
// that will not exist inside the container.
func assertContainerPaths(t *testing.T, name string, comps map[string]config.ComponentConfig, automountServiceAccountToken bool) {
	t.Helper()

	require.NoError(t, checkContainerPaths(name, comps, automountServiceAccountToken))
}

// assertReachableEndpoints fails when a shipped configuration binds an
// endpoint to localhost. Inside a container that is reachable only from the
// container's own network namespace, so a published port, a kubelet probe,
// and a Prometheus scrape all fail.
func assertReachableEndpoints(t *testing.T, name string, cfg *config.Config) {
	t.Helper()

	if cfg.Service.Metrics != nil && cfg.Service.Metrics.Endpoint != "" {
		require.Truef(t,
			strings.HasPrefix(cfg.Service.Metrics.Endpoint, "0.0.0.0:"),
			"%s: service.metrics.endpoint is %q; it must bind 0.0.0.0 to be reachable from outside the container",
			name, cfg.Service.Metrics.Endpoint)
	}

	for id, comp := range cfg.Extensions {
		if comp.ID.Type != "healthcheck" || comp.Config == nil {
			continue
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		require.NoError(t, json.Unmarshal(comp.Config, &body), "%s: %s", name, id)
		if body.Endpoint == "" {
			continue
		}
		require.Truef(t,
			strings.HasPrefix(body.Endpoint, "0.0.0.0:"),
			"%s: %s.endpoint is %q; it must bind 0.0.0.0 for the probes to be reachable",
			name, id, body.Endpoint)
	}

	for id, comp := range cfg.Sources {
		if comp.ID.Type != "http" || comp.Config == nil {
			continue
		}
		var body struct {
			ListenAddress string `json:"listenAddress"`
		}
		require.NoError(t, json.Unmarshal(comp.Config, &body), "%s: %s", name, id)
		if body.ListenAddress == "" {
			continue
		}
		require.Truef(t,
			strings.HasPrefix(body.ListenAddress, "0.0.0.0:"),
			"%s: %s.listenAddress is %q; a container-local bind cannot be reached through a "+
				"published port or a Service. Control exposure on the host side of the port mapping",
			name, id, body.ListenAddress)
	}
}

// parseExample resolves the placeholders a shipped example uses for
// site-specific values and parses the result.
func parseExample(t *testing.T, name string, raw []byte) *config.Config {
	t.Helper()

	resolved := placeholderRE.ReplaceAll(raw, []byte("placeholder.example.com"))
	cfg, err := config.Parse(resolved)
	require.NoErrorf(t, err, "%s failed to parse", name)
	return cfg
}

// TestCheckContainerPathsAutomountedToken pins the one exception to "every
// path must be under the mounted configuration directory" and the condition
// that creates it. The automounted ServiceAccount token exists only where the
// deployment enabled the automount, so accepting the path unconditionally
// would let a Quadlet example, or a chart example that never opted in, ship a
// tokenFile the collector cannot open.
func TestCheckContainerPathsAutomountedToken(t *testing.T) {
	comps := func(field, value string) map[string]config.ComponentConfig {
		return map[string]config.ComponentConfig{
			"bearertokenauth/model-registry": {
				Config: json.RawMessage(
					`{"` + field + `":"` + value + `"}`),
			},
		}
	}

	tests := []struct {
		name        string
		comps       map[string]config.ComponentConfig
		automount   bool
		expectedErr string
	}{
		{
			name:      "When the automount is enabled it should accept the ServiceAccount token path",
			comps:     comps(saTokenField, saTokenFile),
			automount: true,
		},
		{
			name:        "When the automount is disabled it should reject the ServiceAccount token path",
			comps:       comps(saTokenField, saTokenFile),
			automount:   false,
			expectedErr: "automountServiceAccountToken",
		},
		{
			name:        "When the automount is enabled it should still reject other files in the ServiceAccount directory",
			comps:       comps(saTokenField, saTokenDir+"/ca.crt"),
			automount:   true,
			expectedErr: saTokenDir + "/ca.crt",
		},
		{
			name:        "When the automount is enabled it should still reject the token path in another field",
			comps:       comps("certificateAuthority", saTokenFile),
			automount:   true,
			expectedErr: "certificateAuthority",
		},
		{
			name:      "When a path is under the mounted configuration directory it should be accepted",
			comps:     comps(saTokenField, quadletConfigMountPath+"/tokens/model-registry"),
			automount: false,
		},
		{
			name:        "When a path is the host configuration directory it should be rejected",
			comps:       comps(saTokenField, quadletHostConfigDir+"/tokens/model-registry"),
			automount:   true,
			expectedErr: "host path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkContainerPaths("test", tt.comps, tt.automount)
			if tt.expectedErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

// TestQuadletExampleConfigs checks the configurations the RPM installs under
// /usr/share/flightctl/flightctl-catalog-collector/examples.
func TestQuadletExampleConfigs(t *testing.T) {
	exampleDir := filepath.Join(repoRoot(t),
		"deploy", "podman", "flightctl-catalog-collector", "examples")

	entries, err := filepath.Glob(filepath.Join(exampleDir, "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no example configurations found in %s", exampleDir)

	for _, path := range entries {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			cfg := parseExample(t, name, raw)
			decodeAllComponents(t, cfg)

			// A Quadlet unit has no kubelet, so there is no automounted
			// ServiceAccount token: every path has to be one the unit mounts.
			const automountServiceAccountToken = false
			assertContainerPaths(t, name, cfg.Extensions, automountServiceAccountToken)
			assertContainerPaths(t, name, cfg.Sources, automountServiceAccountToken)
			assertContainerPaths(t, name, cfg.Destinations, automountServiceAccountToken)
			assertReachableEndpoints(t, name, cfg)
		})
	}
}

// TestHelmExampleConfigs checks the collector configuration embedded in each
// of the chart's example values files. Helm treats config.content as an
// opaque string, so nothing else in the build would notice a typo in it.
func TestHelmExampleConfigs(t *testing.T) {
	chartDir := filepath.Join(repoRoot(t), "deploy", "helm", "flightctl-catalog-collector")

	paths, err := filepath.Glob(filepath.Join(chartDir, "examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no example values files found in %s", chartDir)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			var values struct {
				Config struct {
					Content string `json:"content"`
				} `json:"config"`
				ServiceAccount struct {
					AutomountServiceAccountToken bool `json:"automountServiceAccountToken"`
				} `json:"serviceAccount"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &values), "%s", name)
			require.NotEmpty(t, values.Config.Content,
				"%s: every shipped example must carry a config.content; the chart default is "+
					"empty on purpose, so an example with none would render nothing usable", name)

			cfg := parseExample(t, name, []byte(values.Config.Content))
			decodeAllComponents(t, cfg)

			// The example only gets to read the automounted token if it also
			// turns the automount on; the chart leaves it off by default.
			automount := values.ServiceAccount.AutomountServiceAccountToken
			assertContainerPaths(t, name, cfg.Extensions, automount)
			assertContainerPaths(t, name, cfg.Sources, automount)
			assertContainerPaths(t, name, cfg.Destinations, automount)
			assertReachableEndpoints(t, name, cfg)
		})
	}
}

// TestChartDefaultConfigIsEmpty pins the decision that the chart ships no
// default pipeline. A default would mean a bare "helm install" deploys a
// pipeline nobody chose.
func TestChartDefaultConfigIsEmpty(t *testing.T) {
	valuesPath := filepath.Join(repoRoot(t),
		"deploy", "helm", "flightctl-catalog-collector", "values.yaml")

	raw, err := os.ReadFile(valuesPath)
	require.NoError(t, err)

	var values struct {
		Config struct {
			Content      string `json:"content"`
			ExistingName string `json:"existingName"`
		} `json:"config"`
		ReplicaCount int `json:"replicaCount"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values))

	require.Empty(t, strings.TrimSpace(values.Config.Content),
		"config.content must default to empty: the collector has no usable default pipeline")
	require.Empty(t, strings.TrimSpace(values.Config.ExistingName),
		"config.existingName must default to empty")
	require.Equal(t, 1, values.ReplicaCount,
		"replicaCount must default to 1: the collector owns the catalogs it writes")
}

// TestServiceAccountTokenMounting pins where each shipped values file gets
// the model registry bearer token from.
//
// The chart default keeps the pod's own token out of the container: a
// collector whose endpoints authenticate from mounted files has no use for
// an API-server credential, and mounting one anyway widens the blast radius
// of every such deployment.
//
// The RHOAI example is the exception, and it is one the deployment shape
// forces. It deploys the collector into the same cluster as the model
// registry, which in RHOAI 3.5.1 sits behind a kube-rbac-proxy running
// without --auth-token-audiences, so TokenReview is asked for the default
// audience and a projected token with a custom audience comes back rejected.
// The pod's automounted token is issued for exactly that audience, is
// rotated automatically by the kubelet, and carries only what the
// ServiceAccount's RoleBinding grants it.
func TestServiceAccountTokenMounting(t *testing.T) {
	chartDir := filepath.Join(repoRoot(t), "deploy", "helm", "flightctl-catalog-collector")

	// The subset of the chart values each case asserts on.
	type chartValues struct {
		Config struct {
			Content string `json:"content"`
		} `json:"config"`
		ServiceAccount struct {
			AutomountServiceAccountToken bool `json:"automountServiceAccountToken"`
		} `json:"serviceAccount"`
		ExtraVolumes []struct {
			Name   string `json:"name"`
			Secret *struct {
				SecretName string `json:"secretName"`
			} `json:"secret"`
			ConfigMap *struct {
				Name string `json:"name"`
			} `json:"configMap"`
		} `json:"extraVolumes"`
		ExtraVolumeMounts []struct {
			Name      string `json:"name"`
			MountPath string `json:"mountPath"`
		} `json:"extraVolumeMounts"`
	}

	readValues := func(t *testing.T, path string) chartValues {
		t.Helper()
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var values chartValues
		require.NoError(t, yaml.Unmarshal(raw, &values), "%s", filepath.Base(path))
		return values
	}

	t.Run("When the chart defaults are used it should not automount the token", func(t *testing.T) {
		values := readValues(t, filepath.Join(chartDir, "values.yaml"))
		require.False(t, values.ServiceAccount.AutomountServiceAccountToken,
			"the generic chart must not mount the pod's ServiceAccount token; "+
				"only a deployment that actually presents it to an API-server-audience "+
				"endpoint should opt in")
	})

	t.Run("When the RHOAI example is used it should automount the token", func(t *testing.T) {
		values := readValues(t, filepath.Join(chartDir, "examples", "values-rhoai-to-flightctl.yaml"))
		require.True(t, values.ServiceAccount.AutomountServiceAccountToken,
			"the RHOAI model registry only accepts an API-server-audience token, "+
				"which is what the automounted ServiceAccount token is")
	})

	t.Run("When the RHOAI example is used it should read the token from the automounted path", func(t *testing.T) {
		values := readValues(t, filepath.Join(chartDir, "examples", "values-rhoai-to-flightctl.yaml"))
		cfg := parseExample(t, "values-rhoai-to-flightctl.yaml", []byte(values.Config.Content))

		comp, ok := cfg.Extensions["bearertokenauth/model-registry"]
		require.True(t, ok, "the model registry bearer token extension must be configured")

		var body struct {
			TokenFile string `json:"tokenFile"`
		}
		require.NoError(t, json.Unmarshal(comp.Config, &body))
		require.Equal(t, saTokenFile, body.TokenFile,
			"the token must come from the kubelet-managed ServiceAccount volume, "+
				"not from a hand-issued Secret")
	})

	t.Run("When the RHOAI example is used it should mount no static model registry credential", func(t *testing.T) {
		values := readValues(t, filepath.Join(chartDir, "examples", "values-rhoai-to-flightctl.yaml"))

		for _, volume := range values.ExtraVolumes {
			require.NotEqual(t, "tokens", volume.Name,
				"the hand-issued model registry token volume must be gone; leaving it "+
					"behind keeps a long-lived credential nothing reads and nobody rotates")
			if volume.Secret != nil {
				require.NotEqual(t, "flightctl-catalog-collector-tokens", volume.Secret.SecretName,
					"the hand-issued model registry token Secret must no longer be mounted")
			}
		}
		for _, mount := range values.ExtraVolumeMounts {
			require.NotEqual(t, "tokens", mount.Name,
				"the hand-issued model registry token mount must be gone")
			require.NotEqual(t, quadletConfigMountPath+"/tokens", mount.MountPath,
				"nothing should be mounted at the former token directory")
		}
	})

	t.Run("When the RHOAI example is used it should keep the Flight Control OAuth2 and CA mounts", func(t *testing.T) {
		values := readValues(t, filepath.Join(chartDir, "examples", "values-rhoai-to-flightctl.yaml"))

		var oauthSecret, caConfigMap string
		for _, volume := range values.ExtraVolumes {
			switch volume.Name {
			case "oauth":
				require.NotNil(t, volume.Secret, "the oauth volume must be backed by a Secret")
				oauthSecret = volume.Secret.SecretName
			case "ca":
				require.NotNil(t, volume.ConfigMap, "the ca volume must be backed by a ConfigMap")
				caConfigMap = volume.ConfigMap.Name
			}
		}
		require.Equal(t, "flightctl-catalog-collector-oauth", oauthSecret,
			"only the model registry credential moved; Flight Control still authenticates "+
				"as a confidential OAuth2 client")
		require.Equal(t, "flightctl-catalog-collector-ca", caConfigMap,
			"both legs still verify TLS against a mounted CA bundle")

		mounts := map[string]string{}
		for _, mount := range values.ExtraVolumeMounts {
			mounts[mount.Name] = mount.MountPath
		}
		require.Equal(t, quadletConfigMountPath+"/oauth", mounts["oauth"])
		require.Equal(t, quadletConfigMountPath+"/certs", mounts["ca"])

		// The CA paths are only useful if the pipeline still points at them.
		cfg := parseExample(t, "values-rhoai-to-flightctl.yaml", []byte(values.Config.Content))
		expectCA := func(comps map[string]config.ComponentConfig, id, want string) {
			comp, ok := comps[id]
			require.Truef(t, ok, "%s must be configured", id)
			var body struct {
				CertificateAuthority string `json:"certificateAuthority"`
			}
			require.NoError(t, json.Unmarshal(comp.Config, &body))
			require.Equalf(t, want, body.CertificateAuthority,
				"%s must keep verifying its peer certificate", id)
		}
		expectCA(cfg.Sources, "kubeflowmodelregistry/rhoai",
			quadletConfigMountPath+"/certs/model-registry-ca.crt")
		expectCA(cfg.Destinations, "flightctl/service",
			quadletConfigMountPath+"/certs/flightctl-ca.crt")
		expectCA(cfg.Extensions, "oauth2client/flightctl",
			quadletConfigMountPath+"/certs/oidc-ca.crt")
	})
}

// TestRunRejectsBadConfig covers the two failure modes the Quadlet unit is
// built around: a configuration that is not there, and one the collector
// cannot use. Both must fail immediately with a clear error rather than
// starting a collector that does nothing.
func TestRunRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name        string
		content     *string
		expectedErr string
	}{
		{
			name:        "When the configuration file is missing it should fail with a read error",
			content:     nil,
			expectedErr: "reading config file",
		},
		{
			name:        "When the configuration is not valid YAML it should fail with a parse error",
			content:     strPtr("sources: [this is not a map\n"),
			expectedErr: "parsing config",
		},
		{
			name:        "When the configuration contains an unknown field it should be rejected",
			content:     strPtr("sources: {}\ndestinations: {}\npipelines: {}\nnotAField: 1\n"),
			expectedErr: "parsing config",
		},
		{
			name: "When a pipeline references a missing source it should be rejected",
			content: strPtr(`sources:
  http/in:
    listenAddress: 0.0.0.0:8080
destinations:
  debug/out: {}
pipelines:
  p:
    source: http/nope
    destination: debug/out
`),
			expectedErr: "loading configuration",
		},
		{
			name: "When a component type has no factory it should fail while building pipelines",
			content: strPtr(`sources:
  nosuchtype/in: {}
destinations:
  debug/out: {}
pipelines:
  p:
    source: nosuchtype/in
    destination: debug/out
`),
			expectedErr: "building pipelines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if tt.content != nil {
				require.NoError(t, os.WriteFile(path, []byte(*tt.content), 0o600))
			}

			err := run(t.Context(), path, components(), testSettings())
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func strPtr(s string) *string { return &s }

// TestChartShipsItsExamples guards the chart's own instructions.
//
// The chart has no default pipeline, so both the render failure in
// templates/_helpers.tpl and the README tell the operator to start from
// examples/values-vanilla.yaml. A .helmignore entry for examples/ stripped
// that directory from the packaged chart, which turned the instruction into
// a dead end for anybody who reached the chart through "helm pull" rather
// than through a clone of this repository.
func TestChartShipsItsExamples(t *testing.T) {
	chartDir := filepath.Join(repoRoot(t), "deploy", "helm", "flightctl-catalog-collector")

	raw, err := os.ReadFile(filepath.Join(chartDir, ".helmignore"))
	require.NoError(t, err)

	for _, line := range strings.Split(string(raw), "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		require.NotEqual(t, "examples", strings.Trim(pattern, "/"),
			"examples/ must stay in the packaged chart: the render failure and the "+
				"README both tell the operator to start from one of these files, and "+
				"after a \"helm pull\" they would not be there")
	}

	// The instruction is only useful if the file it names exists.
	referenced := filepath.Join(chartDir, "examples", "values-vanilla.yaml")
	_, err = os.Stat(referenced)
	require.NoErrorf(t, err, "the chart's error message points at %s", referenced)

	helpers, err := os.ReadFile(filepath.Join(chartDir, "templates", "_helpers.tpl"))
	require.NoError(t, err)
	require.Contains(t, string(helpers), "examples/values-vanilla.yaml",
		"the render failure should keep naming the example to start from")
}

// TestChartMetricsPathIsNotAValue pins the removal of metrics.path.
//
// The collector serves its Prometheus registry at a fixed /metrics and
// service.metrics.endpoint in its configuration is a host:port pair with no
// path component. A chart value could therefore only have moved the scrape
// target in the Service annotations and in the ServiceMonitor onto a path the
// collector answers with 404, losing metrics without reporting an error.
func TestChartMetricsPathIsNotAValue(t *testing.T) {
	chartDir := filepath.Join(repoRoot(t), "deploy", "helm", "flightctl-catalog-collector")

	// Both the generated file and the source it is generated from, so the
	// value cannot reappear on the next regeneration. The source is not YAML
	// until its template actions are gone: drop the comment blocks outright
	// and stand a scalar in for the rest.
	tplCommentRE := regexp.MustCompile(`(?s)\{\{/\*.*?\*/\s*-?\}\}\n?`)
	tplActionRE := regexp.MustCompile(`(?s)\{\{.*?\}\}`)

	for _, name := range []string{"values.yaml", "values.yaml.gotmpl"} {
		raw, err := os.ReadFile(filepath.Join(chartDir, name))
		require.NoError(t, err)
		raw = tplActionRE.ReplaceAll(tplCommentRE.ReplaceAll(raw, nil), []byte("generated"))

		var values struct {
			Metrics map[string]any `json:"metrics"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &values), "%s", name)
		require.NotEmpty(t, values.Metrics, "%s: the metrics block must still exist", name)
		require.NotContains(t, values.Metrics, "path",
			"%s: metrics.path must not come back; the collector hardcodes /metrics and a "+
				"chart value could only point Prometheus at a 404", name)
	}
}

// TestChartIsGeneratedFromBuildProfile checks that the committed Chart.yaml
// and values.yaml still agree with the community-el9 build profile they are
// generated from.
//
// Both files are rendered by deploy/helm/cmd/charttmpl, which is what lets a
// downstream build rebrand the chart and repoint it at its own registry. A
// hand-edit here, or a profile change that was never regenerated, produces a
// chart that silently reverts on the next "go generate".
func TestChartIsGeneratedFromBuildProfile(t *testing.T) {
	root := repoRoot(t)
	chartDir := filepath.Join(root, "deploy", "helm", "flightctl-catalog-collector")

	raw, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "helm-chart-opts.yaml"))
	require.NoError(t, err)

	type imageSpec struct {
		Image string `json:"image"`
	}
	type profile struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Home        string `json:"home"`
		Icon        string `json:"icon"`
		Annotations struct {
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			SupportURL string `json:"supportURL"`
		} `json:"annotations"`
		Images map[string]imageSpec `json:"images"`
	}

	var profiles map[string]profile
	require.NoError(t, yaml.Unmarshal(raw, &profiles))

	// Every variant the generator can be asked for must have a collector
	// profile; a missing one would silently fall back to community-el9 and
	// ship a downstream chart pointing at quay.io.
	for _, variant := range []string{"community-el9", "community-el10", "rhem-el9", "rhem-el10"} {
		key := "catalog-collector-" + variant
		p, ok := profiles[key]
		require.Truef(t, ok, "helm-chart-opts.yaml must carry a %q profile", key)
		require.NotEmptyf(t, p.Images["catalogCollector"].Image,
			"%s: images.catalogCollector.image must be set", key)
		require.NotEmptyf(t, p.Name, "%s: name must be set", key)
		require.NotEmptyf(t, p.Icon, "%s: icon must be set", key)
	}

	// The committed files are the community-el9 rendering, which is the
	// generator's default and its fallback.
	want := profiles["catalog-collector-community-el9"]

	chartRaw, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	require.NoError(t, err)
	var chart struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Home        string            `json:"home"`
		Icon        string            `json:"icon"`
		Annotations map[string]string `json:"annotations"`
	}
	require.NoError(t, yaml.Unmarshal(chartRaw, &chart))

	require.Equal(t, want.Name, chart.Name)
	require.Equal(t, want.Description, chart.Description)
	require.Equal(t, want.Home, chart.Home)
	require.Equal(t, want.Icon, chart.Icon)
	require.Equal(t, want.Annotations.Name, chart.Annotations["charts.openshift.io/name"])
	require.Equal(t, want.Annotations.Provider, chart.Annotations["charts.openshift.io/provider"])
	require.Equal(t, want.Annotations.SupportURL, chart.Annotations["charts.openshift.io/supportURL"])

	valuesRaw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	require.NoError(t, err)
	var values struct {
		Image struct {
			Image string `json:"image"`
		} `json:"image"`
	}
	require.NoError(t, yaml.Unmarshal(valuesRaw, &values))
	require.Equal(t, want.Images["catalogCollector"].Image, values.Image.Image,
		"values.yaml is generated from the build profile; regenerate with "+
			"\"go generate ./deploy/helm/...\" instead of editing it")
}

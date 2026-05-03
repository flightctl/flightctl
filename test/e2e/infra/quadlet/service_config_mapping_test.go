package quadlet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/pkg/template"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// Example per-service config for imagebuilder-worker, matching the structure
// produced by the template from /etc/flightctl/service-config.yaml into
// /etc/flightctl/flightctl-imagebuilder-worker/config.yaml.
const exampleImageBuilderWorkerPerServiceYAML = `
database:
  hostname: flightctl-db
  type: pgsql
  port: 5432
  name: flightctl
kv:
  hostname: flightctl-kv
  port: 6379
service:
  baseAgentEndpointUrl: https://example.com:7443/
  baseUIUrl: https://example.com:443
imageBuilderWorker:
  logLevel: info
  maxConcurrentBuilds: 4
  defaultTTL: 168h
  rpmRepoUrl: ""
  rpmRepoAdd: true
  rpmRepoEnable: ""
  serviceImages:
    podman:
      image: quay.io/org/podman:latest
      skipTlsVerify: false
    bootcImageBuilder:
      image: quay.io/org/bootc:latest
      skipTlsVerify: false
    syft:
      image: quay.io/org/syft:latest
      skipTlsVerify: false
`

// findRepoRoot returns the repository root (directory containing go.mod).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

// templatePathForService returns the path to the service's config.yaml.template under deploy/podman.
func templatePathForService(t *testing.T, service infra.ServiceName) string {
	t.Helper()
	info := GetServiceInfo(service)
	return filepath.Join(findRepoRoot(t), "deploy", "podman", info.ContainerName, info.ContainerName+"-config", "config.yaml.template")
}

// renderAllMappedServices reads service-config.yaml from configDir and renders each mapped service's template into configDir/<container>/config.yaml.
func renderAllMappedServices(t *testing.T, configDir string) {
	t.Helper()
	serviceConfigPath := filepath.Join(configDir, "service-config.yaml")
	data, err := os.ReadFile(serviceConfigPath)
	require.NoError(t, err)
	var serviceConfig map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &serviceConfig))

	for _, service := range ServicesWithServiceConfigMappings() {
		tplPath := templatePathForService(t, service)
		if _, err := os.Stat(tplPath); err != nil {
			t.Skipf("template required for test (run from repo root): %s: %v", tplPath, err)
		}
		info := GetServiceInfo(service)
		outPath := filepath.Join(configDir, info.ContainerName, "config.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(outPath), 0755))
		require.NoError(t, template.RenderWithData(serviceConfig, tplPath, outPath))
	}
}

// TestSetServiceConfigPersistsAfterRerender uses only GetServiceConfig and SetServiceConfig
// with a tmp dir. Initial configs are generated from service-config.yaml (no pre-written
// per-service files). For each service with a mapping, it sets a change, re-renders from
// service-config.yaml (as on restart), then verifies the change is reflected.
func TestSetServiceConfigPersistsAfterRerender(t *testing.T) {
	// Baseline service-config.yaml content; same layout as /etc/flightctl/service-config.yaml.
	initialServiceConfig := `
global:
  baseDomain: test.example.com
  generateCertificates: builtin
db:
  name: flightctl
  type: builtin
imagebuilderWorker:
  logLevel: info
  maxConcurrentBuilds: 2
  defaultTTL: 168h
telemetryGateway:
  forward:
    endpoint:
`

	for _, service := range ServicesWithServiceConfigMappings() {
		t.Run(string(service), func(t *testing.T) {
			tmpDir := t.TempDir()
			serviceConfigPath := filepath.Join(tmpDir, "service-config.yaml")
			require.NoError(t, os.WriteFile(serviceConfigPath, []byte(initialServiceConfig), 0600))
			renderAllMappedServices(t, tmpDir)
			p := NewInfraProvider(tmpDir, "", false)

			// 1) Get current config (from generated file)
			content, err := p.GetServiceConfig(service)
			require.NoError(t, err)
			require.NotEmpty(t, content)

			var cfg map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(content), &cfg))

			// 2) Change a value and SetServiceConfig (writes to service-config.yaml for mapped services)
			var sectionKey string
			var setValue interface{}
			var getSection func(map[string]interface{}) interface{}
			var assertValue func(t *testing.T, section interface{})

			switch service {
			case infra.ServiceImageBuilderWorker:
				sectionKey = "imageBuilderWorker"
				setValue = float64(6)
				getSection = func(m map[string]interface{}) interface{} { return m["imageBuilderWorker"] }
				assertValue = func(t *testing.T, section interface{}) {
					s, ok := section.(map[string]interface{})
					require.True(t, ok)
					assert.Equal(t, float64(6), s["maxConcurrentBuilds"])
				}
			case infra.ServiceTelemetryGateway:
				sectionKey = "telemetryGateway"
				setValue = map[string]interface{}{
					"endpoint": "https://otel.example.com:4317",
				}
				getSection = func(m map[string]interface{}) interface{} { return m["telemetryGateway"] }
				assertValue = func(t *testing.T, section interface{}) {
					s, ok := section.(map[string]interface{})
					require.True(t, ok)
					forward, ok := s["forward"].(map[string]interface{})
					require.True(t, ok)
					assert.Equal(t, "https://otel.example.com:4317", forward["endpoint"])
				}
			default:
				t.Skip("no test case for service")
				return
			}

			section, _ := getSection(cfg).(map[string]interface{})
			require.NotNil(t, section, "section %q not found in config", sectionKey)
			// Set the field we care about (maxConcurrentBuilds or forward)
			if service == infra.ServiceImageBuilderWorker {
				section["maxConcurrentBuilds"] = setValue
			} else {
				section["forward"] = setValue
			}
			updated, err := yaml.Marshal(cfg)
			require.NoError(t, err)
			require.NoError(t, p.SetServiceConfig(service, "config.yaml", string(updated)))

			// 3) Re-render all from service-config.yaml (as on restart)
			renderAllMappedServices(t, tmpDir)

			// 4) Get config again; change should be reflected
			content2, err := p.GetServiceConfig(service)
			require.NoError(t, err)
			var cfg2 map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(content2), &cfg2))
			section2 := getSection(cfg2)
			require.NotNil(t, section2)
			assertValue(t, section2)
		})
	}
}

func TestApplyServiceConfigMappings_ImageBuilderWorker(t *testing.T) {
	updates, err := applyServiceConfigMappings(infra.ServiceImageBuilderWorker, exampleImageBuilderWorkerPerServiceYAML)
	require.NoError(t, err)
	require.NotNil(t, updates)

	// Should have exactly one key: imagebuilderWorker (service-config key)
	assert.Len(t, updates, 1)
	section, ok := updates["imagebuilderWorker"]
	require.True(t, ok, "updates should contain imagebuilderWorker")

	// Section should match the imageBuilderWorker subtree from the per-service config
	sub, ok := section.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "info", sub["logLevel"])
	// YAML unmarshaling produces float64 for numbers
	assert.Equal(t, float64(4), sub["maxConcurrentBuilds"])
	assert.Equal(t, "168h", sub["defaultTTL"])
	assert.Equal(t, true, sub["rpmRepoAdd"])
	serviceImages, ok := sub["serviceImages"].(map[string]interface{})
	require.True(t, ok)
	podman, ok := serviceImages["podman"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "quay.io/org/podman:latest", podman["image"])
	assert.Equal(t, false, podman["skipTlsVerify"])
}

func TestApplyServiceConfigMappings_KeyNormalization(t *testing.T) {
	// Per-service YAML with lowercase "imagebuilderWorker" (as some marshallers produce)
	// should still be found and mapped to service-config key imagebuilderWorker.
	yamlWithLowercaseKey := `
database: {}
kv: {}
service: {}
imagebuilderWorker:
  logLevel: debug
  maxConcurrentBuilds: 1
`
	updates, err := applyServiceConfigMappings(infra.ServiceImageBuilderWorker, yamlWithLowercaseKey)
	require.NoError(t, err)
	require.NotNil(t, updates)
	section, ok := updates["imagebuilderWorker"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "debug", section["logLevel"])
	assert.Equal(t, float64(1), section["maxConcurrentBuilds"])
}

func TestApplyServiceConfigMappings_NoMappingReturnsNil(t *testing.T) {
	updates, err := applyServiceConfigMappings(infra.ServiceAPI, exampleImageBuilderWorkerPerServiceYAML)
	require.NoError(t, err)
	assert.Nil(t, updates)
}

func TestApplyServiceConfigMappings_InvalidYAML(t *testing.T) {
	_, err := applyServiceConfigMappings(infra.ServiceImageBuilderWorker, "not: valid: yaml: [")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse per-service config")
}

func TestApplyServiceConfigMappings_DeepCopy(t *testing.T) {
	// Ensure we don't mutate the original parsed map when merging
	yaml := `
imageBuilderWorker:
  logLevel: info
  maxConcurrentBuilds: 2
`
	updates, err := applyServiceConfigMappings(infra.ServiceImageBuilderWorker, yaml)
	require.NoError(t, err)
	require.NotNil(t, updates)
	section := updates["imagebuilderWorker"].(map[string]interface{})
	section["maxConcurrentBuilds"] = 999
	// Second call should still see original values in its parse
	updates2, err := applyServiceConfigMappings(infra.ServiceImageBuilderWorker, yaml)
	require.NoError(t, err)
	section2 := updates2["imagebuilderWorker"].(map[string]interface{})
	assert.Equal(t, float64(2), section2["maxConcurrentBuilds"], "deep copy should not share reference with first result")
}

func TestGetSubtreeWithKeyNormalization(t *testing.T) {
	m := map[string]interface{}{
		"imageBuilderWorker": map[string]interface{}{"logLevel": "info"},
		"other":              "x",
	}
	assert.Equal(t, map[string]interface{}{"logLevel": "info"}, getSubtreeWithKeyNormalization(m, "imageBuilderWorker"))
	assert.Nil(t, getSubtreeWithKeyNormalization(m, "missing"))
	// lowerFirst("imageBuilderWorker") = "imagebuilderWorker" - not in m, but we have imageBuilderWorker
	// so first key wins
	assert.NotNil(t, getSubtreeWithKeyNormalization(m, "imageBuilderWorker"))

	// Key in map uses lowercase 'b' (as in service-config.yaml); lookup uses camelCase.
	m2 := map[string]interface{}{
		"imagebuilderWorker": map[string]interface{}{"logLevel": "debug"},
	}
	got := getSubtreeWithKeyNormalization(m2, "imageBuilderWorker")
	require.NotNil(t, got)
	assert.Equal(t, map[string]interface{}{"logLevel": "debug"}, got)
}

func TestLowerFirst(t *testing.T) {
	assert.Equal(t, "imageBuilderWorker", lowerFirst("ImageBuilderWorker"))
	assert.Equal(t, "imagebuilderWorker", lowerFirst("imagebuilderWorker"))
	assert.Equal(t, "", lowerFirst(""))
	assert.Equal(t, "a", lowerFirst("A"))
}

func TestFirstUpperToLower(t *testing.T) {
	assert.Equal(t, "imagebuilderWorker", firstUpperToLower("imageBuilderWorker"))
	assert.Equal(t, "imagebuilderworker", firstUpperToLower("imagebuilderWorker"))
	assert.Equal(t, "", firstUpperToLower(""))
	assert.Equal(t, "abC", firstUpperToLower("aBC"))
}

func TestDeepCopyMap(t *testing.T) {
	orig := map[string]interface{}{
		"a": 1,
		"b": map[string]interface{}{"c": 2},
		"d": []interface{}{3, 4},
	}
	copied := deepCopyMap(orig).(map[string]interface{})
	assert.Equal(t, 1, copied["a"])
	copied["a"] = 99
	assert.Equal(t, 1, orig["a"], "original should be unchanged")

	inner := copied["b"].(map[string]interface{})
	inner["c"] = 100
	assert.Equal(t, 2, orig["b"].(map[string]interface{})["c"], "nested map should be copied")
}

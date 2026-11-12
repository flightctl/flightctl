package flightctl_test

import (
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func catalogChart(t *testing.T, settings ...string) (string, error) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	args := []string{"template", "flightctl", filepath.Dir(file), "--namespace", "public", "--set-string", "global.enableOpenShiftExtensions=false", "--set-string", "global.exposeServicesMethod=none", "--set-string", "global.auth.type=none", "--set-string", "global.generateCertificates=none", "--set-string", "global.baseDomain=example.test"}
	for _, setting := range settings {
		args = append(args, "--set-string", setting)
	}
	output, err := exec.Command("helm", args...).CombinedOutput()
	return string(output), err
}
func catalogDocuments(t *testing.T, output string) map[string]map[string]any {
	t.Helper()
	result := map[string]map[string]any{}
	decoder := yaml.NewDecoder(strings.NewReader(output))
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if len(doc) == 0 {
			continue
		}
		metadata := doc["metadata"].(map[string]any)
		result[doc["kind"].(string)+"/"+metadata["name"].(string)] = doc
	}
	return result
}
func namedEntry(entries any, name string) map[string]any {
	values, _ := entries.([]any)
	for _, value := range values {
		entry := value.(map[string]any)
		if entry["name"] == name {
			return entry
		}
	}
	return nil
}
func TestUpdaterCatalogMounts(t *testing.T) {
	for _, tc := range []struct{ name, configMap, key, internal string }{{"When disabled it should use retained DB state without a mount", "", "updaters.yaml", ""}, {"When configured it should mount identical input for all authorization runtimes", "firmware-updaters-r1", "updaters.yaml", ""}, {"When namespaces differ it should reference the admin map within each pod namespace", "firmware-updaters-r2", "catalog.yaml", "private"}} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := catalogChart(t, "global.firmwareUpdaterCatalog.configMapName="+tc.configMap, "global.firmwareUpdaterCatalog.key="+tc.key, "global.internalNamespace="+tc.internal)
			require.NoError(t, err, output)
			docs := catalogDocuments(t, output)
			require.NotContains(t, docs, "ConfigMap/"+tc.configMap)
			for _, component := range []string{"api", "worker", "periodic"} {
				name := "flightctl-" + component
				deployment := docs["Deployment/"+name]
				require.NotNil(t, deployment)
				config := docs["ConfigMap/"+name+"-config"]
				require.NotNil(t, config)
				raw := config["data"].(map[string]any)["config.yaml"].(string)
				var runtimeConfig map[string]any
				require.NoError(t, yaml.Unmarshal([]byte(raw), &runtimeConfig))
				pod := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				volume := namedEntry(pod["volumes"], "firmware-updater-catalog")
				containerName := component
				if component != "periodic" {
					containerName = name
				}
				container := namedEntry(pod["containers"], containerName)
				require.NotNil(t, container)
				mount := namedEntry(container["volumeMounts"], "firmware-updater-catalog")
				if tc.configMap == "" {
					require.NotContains(t, runtimeConfig, "firmwareUpdaterCatalogFile")
					require.Nil(t, volume)
					require.Nil(t, mount)
					continue
				}
				require.Equal(t, "/etc/flightctl/firmware/updaters.yaml", runtimeConfig["firmwareUpdaterCatalogFile"])
				require.NotNil(t, mount)
				require.Equal(t, true, mount["readOnly"])
				require.Equal(t, "/etc/flightctl/firmware", mount["mountPath"])
				require.NotNil(t, volume)
				source := volume["configMap"].(map[string]any)
				require.Equal(t, tc.configMap, source["name"])
				items := source["items"].([]any)
				require.Len(t, items, 1)
				item := items[0].(map[string]any)
				require.Equal(t, tc.key, item["key"])
				require.Equal(t, "updaters.yaml", item["path"])
				namespace := "public"
				if component != "api" && tc.internal != "" {
					namespace = tc.internal
				}
				require.Equal(t, namespace, deployment["metadata"].(map[string]any)["namespace"])
			}
		})
	}
}
func TestUpdaterCatalogInvalidValues(t *testing.T) {
	for _, setting := range []string{"global.firmwareUpdaterCatalog.configMapName=bad/name", "global.firmwareUpdaterCatalog.key=../catalog.yaml", "global.firmwareUpdaterCatalog.unknown=value"} {
		t.Run("When "+setting+" it should reject", func(t *testing.T) {
			output, err := catalogChart(t, setting)
			require.Error(t, err, output)
			require.Contains(t, output, "firmwareUpdaterCatalog")
		})
	}
}

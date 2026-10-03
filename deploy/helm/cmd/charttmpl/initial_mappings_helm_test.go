package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const initialMappingsPath = "/etc/flightctl/label-sync/mappings.yaml"

type helmManifestResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Template struct {
			Spec helmPodSpec `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type helmPodSpec struct {
	Containers     []helmContainer `yaml:"containers"`
	InitContainers []helmContainer `yaml:"initContainers"`
	Volumes        []helmVolume    `yaml:"volumes"`
}

type helmContainer struct {
	Name         string            `yaml:"name"`
	VolumeMounts []helmVolumeMount `yaml:"volumeMounts"`
}

type helmVolumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	ReadOnly  bool   `yaml:"readOnly"`
	SubPath   string `yaml:"subPath"`
}

type helmVolume struct {
	Name      string `yaml:"name"`
	ConfigMap *struct {
		Name string `yaml:"name"`
	} `yaml:"configMap"`
}

type initialMapping struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   initialMappingMetadata `yaml:"metadata"`
	Spec       initialMappingSpec     `yaml:"spec"`
}

type initialMappingMetadata struct {
	Name string `yaml:"name"`
}

type initialMappingSpec struct {
	ResourceType string `yaml:"resourceType"`
	Expression   string `yaml:"expression"`
}

func TestInitialLabelSyncMappingHelmRendering(t *testing.T) {
	helmPath, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; make lint-helm runs the chart rendering checks")
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate chart test source")
	}
	chartDir := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../flightctl"))

	defaultMappings := loadPackagedMappings(t, chartDir)

	t.Run("defaults render for all runtime and migration consumers", func(t *testing.T) {
		resources := renderChart(t, helmPath, chartDir, "", false)
		assertMappingConfigMaps(t, resources, map[string][]initialMapping{"runtime": defaultMappings})
		assertMappingDefaultsMatchPackagedManifest(t, chartDir, resources, defaultMappings)
		assertInitialMappingConsumers(t, resources, "runtime")
	})

	t.Run("replacement sequence is copied to runtime and internal namespaces", func(t *testing.T) {
		values := writeHelmValues(t, `global:
  internalNamespace: internal
organizations:
  initialLabelSyncMappings:
    - apiVersion: flightctl.io/v1beta1
      kind: LabelSyncMapping
      metadata:
        name: custom-mapping
      spec:
        resourceType: Device
        expression: "{}"
`)
		resources := renderChart(t, helmPath, chartDir, values, false)
		customMappings := []initialMapping{{
			APIVersion: "flightctl.io/v1beta1",
			Kind:       "LabelSyncMapping",
			Metadata:   initialMappingMetadata{Name: "custom-mapping"},
			Spec:       initialMappingSpec{ResourceType: "Device", Expression: "{}"},
		}}
		assertMappingConfigMaps(t, resources, map[string][]initialMapping{
			"runtime":  customMappings,
			"internal": customMappings,
		})
		assertInitialMappingConsumers(t, resources, "internal")
	})

	t.Run("empty sequence disables automatic mappings", func(t *testing.T) {
		values := writeHelmValues(t, "organizations:\n  initialLabelSyncMappings: []\n")
		resources := renderChart(t, helmPath, chartDir, values, false)
		assertMappingConfigMaps(t, resources, map[string][]initialMapping{"runtime": {}})
		assertInitialMappingConsumers(t, resources, "runtime")
	})

	t.Run("upgrade migration jobs use the pre-upgrade mappings ConfigMap", func(t *testing.T) {
		values := writeHelmValues(t, `global:
  internalNamespace: internal
`)
		resources := renderChart(t, helmPath, chartDir, values, true)
		defaultMappings := loadPackagedMappings(t, chartDir)
		assertMappingConfigMaps(t, resources, map[string][]initialMapping{
			"runtime":  defaultMappings,
			"internal": defaultMappings,
		})

		const migrationConfigMapName = "flightctl-initial-label-sync-mappings-migration"
		foundMigrationConfigMap := false
		migrationJobs := 0
		for _, resource := range resources {
			if resource.Kind == "ConfigMap" && resource.Metadata.Name == migrationConfigMapName {
				foundMigrationConfigMap = true
				if resource.Metadata.Namespace != "internal" {
					t.Errorf("migration ConfigMap namespace = %q, want internal", resource.Metadata.Namespace)
				}
				if resource.Metadata.Annotations["helm.sh/hook"] != "pre-upgrade" {
					t.Errorf("migration ConfigMap hook = %q, want pre-upgrade", resource.Metadata.Annotations["helm.sh/hook"])
				}
				if resource.Metadata.Annotations["helm.sh/hook-weight"] != "-30" {
					t.Errorf("migration ConfigMap hook weight = %q, want -30", resource.Metadata.Annotations["helm.sh/hook-weight"])
				}
				if resource.Metadata.Annotations["helm.sh/hook-delete-policy"] != "before-hook-creation,hook-succeeded" {
					t.Errorf("migration ConfigMap delete policy = %q, want before-hook-creation,hook-succeeded", resource.Metadata.Annotations["helm.sh/hook-delete-policy"])
				}
				var mappings []initialMapping
				if err := yaml.Unmarshal([]byte(resource.Data["mappings.yaml"]), &mappings); err != nil {
					t.Errorf("decode upgrade migration mappings: %v", err)
				} else {
					assertInitialMappingsEqual(t, "upgrade migration ConfigMap", mappings, defaultMappings)
				}
			}
			if resource.Kind == "Job" && strings.HasPrefix(resource.Metadata.Name, "flightctl-db-migration-") {
				migrationJobs++
				if !assertNamedContainerMappingSourceMount(t, resource, "run-migrations", migrationConfigMapName) {
					t.Errorf("upgrade migration job %s does not mount the pre-upgrade mappings ConfigMap", resource.Metadata.Name)
				}
			}
		}
		if !foundMigrationConfigMap {
			t.Errorf("missing pre-upgrade migration ConfigMap %q", migrationConfigMapName)
		}
		if migrationJobs == 0 {
			t.Error("missing upgrade database migration jobs")
		}
	})
}

func assertMappingDefaultsMatchPackagedManifest(t *testing.T, chartDir string, resources []helmManifestResource, expected []initialMapping) {
	t.Helper()
	assertInitialMappingsEqual(t, "packaged manifest", loadPackagedMappings(t, chartDir), expected)

	for _, valuesFile := range []string{"values.yaml", "values.yaml.gotmpl"} {
		valuesPath := filepath.Join(chartDir, valuesFile)
		valuesContent, err := os.ReadFile(valuesPath)
		if err != nil {
			t.Fatal(err)
		}
		var values struct {
			Organizations struct {
				InitialLabelSyncMappings []initialMapping `yaml:"initialLabelSyncMappings"`
			} `yaml:"organizations"`
		}
		valuesToDecode := valuesContent
		if valuesFile == "values.yaml.gotmpl" {
			const startMarker = "\norganizations:\n"
			const endMarker = "\n# -- API Server Configuration"
			start := strings.Index(string(valuesContent), startMarker)
			if start < 0 {
				t.Fatalf("%s has no organizations defaults section", valuesFile)
			}
			start++
			end := strings.Index(string(valuesContent[start:]), endMarker)
			if end < 0 {
				t.Fatalf("%s organizations section has no end marker", valuesFile)
			}
			valuesToDecode = valuesContent[start : start+end]
		}
		if err := yaml.Unmarshal(valuesToDecode, &values); err != nil {
			t.Fatalf("decode %s initial mapping defaults: %v", valuesFile, err)
		}
		assertInitialMappingsEqual(t, valuesFile, values.Organizations.InitialLabelSyncMappings, expected)
	}

	for _, resource := range resources {
		if resource.Kind != "ConfigMap" || resource.Metadata.Name != "flightctl-initial-label-sync-mappings" || resource.Metadata.Namespace != "runtime" {
			continue
		}
		var renderedMappings []initialMapping
		if err := yaml.Unmarshal([]byte(resource.Data["mappings.yaml"]), &renderedMappings); err != nil {
			t.Fatalf("decode rendered Helm mapping defaults: %v", err)
		}
		assertInitialMappingsEqual(t, "rendered Helm ConfigMap", renderedMappings, expected)
		return
	}
	t.Fatal("rendered Helm mapping ConfigMap is missing from runtime namespace")
}

func loadPackagedMappings(t *testing.T, chartDir string) []initialMapping {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join(chartDir, "../../.."))
	packagedPath := filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml")
	packagedContent, err := os.ReadFile(packagedPath)
	if err != nil {
		t.Fatal(err)
	}
	var packagedMappings []initialMapping
	if err := yaml.Unmarshal(packagedContent, &packagedMappings); err != nil {
		t.Fatalf("decode packaged mapping defaults: %v", err)
	}
	return packagedMappings
}

func assertInitialMappingsEqual(t *testing.T, source string, got, want []initialMapping) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s has %d mappings, want %d", source, len(got), len(want))
	}
	for i := range want {
		if got[i].APIVersion != want[i].APIVersion || got[i].Kind != want[i].Kind || got[i].Metadata.Name != want[i].Metadata.Name || got[i].Spec.ResourceType != want[i].Spec.ResourceType {
			t.Errorf("%s mapping %d does not match the expected API version, kind, name, or resource type", source, i)
		}
		gotExpression := strings.Join(strings.Fields(got[i].Spec.Expression), " ")
		wantExpression := strings.Join(strings.Fields(want[i].Spec.Expression), " ")
		if gotExpression != wantExpression {
			t.Errorf("%s mapping %q expression = %q, want %q", source, got[i].Metadata.Name, got[i].Spec.Expression, want[i].Spec.Expression)
		}
	}
}

func renderChart(t *testing.T, helmPath, chartDir, valuesPath string, isUpgrade bool) []helmManifestResource {
	t.Helper()
	args := []string{
		"template", "flightctl", chartDir, "--namespace", "runtime",
		"--values", filepath.Join(chartDir, "lint-values.yaml"),
	}
	if valuesPath != "" {
		args = append(args, "--values", valuesPath)
	}
	if isUpgrade {
		args = append(args, "--is-upgrade")
	}
	cmd := exec.Command(helmPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(output)))
	var resources []helmManifestResource
	for {
		var resource helmManifestResource
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
	return resources
}

func writeHelmValues(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertMappingConfigMaps(t *testing.T, resources []helmManifestResource, expectedByNamespace map[string][]initialMapping) {
	t.Helper()
	const name = "flightctl-initial-label-sync-mappings"
	found := make(map[string]bool, len(expectedByNamespace))
	for _, resource := range resources {
		if resource.Kind != "ConfigMap" || resource.Metadata.Name != name {
			continue
		}
		expected, ok := expectedByNamespace[resource.Metadata.Namespace]
		if !ok {
			t.Errorf("unexpected initial mapping ConfigMap namespace %q", resource.Metadata.Namespace)
			continue
		}
		found[resource.Metadata.Namespace] = true

		var mappings []initialMapping
		if err := yaml.Unmarshal([]byte(resource.Data["mappings.yaml"]), &mappings); err != nil {
			t.Errorf("decode mappings.yaml in namespace %q: %v", resource.Metadata.Namespace, err)
			continue
		}
		if len(mappings) != len(expected) {
			t.Errorf("namespace %q got %d mappings, want %d", resource.Metadata.Namespace, len(mappings), len(expected))
			continue
		}
		for i, mapping := range mappings {
			if mapping.APIVersion != expected[i].APIVersion || mapping.Kind != expected[i].Kind || mapping.Metadata.Name != expected[i].Metadata.Name || mapping.Spec.ResourceType != expected[i].Spec.ResourceType {
				t.Errorf("namespace %q mapping %d = %q %q %q/%q, want %q %q %q/%q", resource.Metadata.Namespace, i, mapping.APIVersion, mapping.Kind, mapping.Metadata.Name, mapping.Spec.ResourceType, expected[i].APIVersion, expected[i].Kind, expected[i].Metadata.Name, expected[i].Spec.ResourceType)
			}
			gotExpression := strings.Join(strings.Fields(mapping.Spec.Expression), " ")
			wantExpression := strings.Join(strings.Fields(expected[i].Spec.Expression), " ")
			if gotExpression != wantExpression {
				t.Errorf("namespace %q mapping %q expression = %q, want %q", resource.Metadata.Namespace, mapping.Metadata.Name, gotExpression, wantExpression)
			}
		}
	}
	for namespace := range expectedByNamespace {
		if !found[namespace] {
			t.Errorf("missing initial mapping ConfigMap in namespace %q", namespace)
		}
	}
}

func assertInitialMappingConsumers(t *testing.T, resources []helmManifestResource, migrationNamespace string) {
	t.Helper()
	configMaps := map[string]bool{
		"flightctl-api-config":                true,
		"flightctl-imagebuilder-api-config":   true,
		"flightctl-alertmanager-proxy-config": true,
		"flightctl-remote-access-config":      true,
		"flightctl-db-migration-config":       true,
	}
	for configName := range configMaps {
		found := false
		for _, resource := range resources {
			if resource.Kind != "ConfigMap" || resource.Metadata.Name != configName {
				continue
			}
			found = true
			if !strings.Contains(resource.Data["config.yaml"], "initialLabelSyncMappingsFile: "+initialMappingsPath) {
				t.Errorf("%s config does not point to %s", configName, initialMappingsPath)
			}
		}
		if !found {
			t.Errorf("missing configmap %s", configName)
		}
	}

	expectedDeployments := map[string]string{
		"flightctl-api":                "flightctl-api",
		"flightctl-imagebuilder-api":   "flightctl-imagebuilder-api",
		"flightctl-alertmanager-proxy": "alertmanager-proxy",
		"flightctl-remote-access":      "flightctl-remote-access",
	}
	var migrationJobs []helmManifestResource
	for _, resource := range resources {
		if resource.Kind == "Deployment" {
			if containerName, ok := expectedDeployments[resource.Metadata.Name]; ok {
				if !assertNamedContainerMappingSourceMount(t, resource, containerName, "flightctl-initial-label-sync-mappings") {
					t.Errorf("deployment %s does not have its mapping source mounted on container %s", resource.Metadata.Name, containerName)
				}
				delete(expectedDeployments, resource.Metadata.Name)
			}
		}
		if resource.Kind == "Job" && strings.HasPrefix(resource.Metadata.Name, "flightctl-db-migration-") {
			migrationJobs = append(migrationJobs, resource)
			if resource.Metadata.Namespace != migrationNamespace {
				t.Errorf("migration job %s runs in namespace %q, want %q", resource.Metadata.Name, resource.Metadata.Namespace, migrationNamespace)
			}
			if !assertNamedContainerMappingSourceMount(t, resource, "run-migrations", "flightctl-initial-label-sync-mappings") {
				t.Errorf("migration job %s does not have its mapping source mounted on run-migrations", resource.Metadata.Name)
			}
		}
	}
	for name := range expectedDeployments {
		t.Errorf("missing consumer deployment %s", name)
	}
	if len(migrationJobs) == 0 {
		t.Error("missing database migration job")
	}
}

func assertNamedContainerMappingSourceMount(t *testing.T, resource helmManifestResource, containerName, configMapName string) bool {
	t.Helper()
	const volumeName = "flightctl-initial-label-sync-mappings"
	const mountPath = "/etc/flightctl/label-sync"
	volumes := make(map[string]bool, len(resource.Spec.Template.Spec.Volumes))
	for _, volume := range resource.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName && volume.ConfigMap != nil && volume.ConfigMap.Name == configMapName {
			volumes[volume.Name] = true
		}
	}
	if !volumes[volumeName] {
		t.Errorf("%s %s does not use the initial mappings ConfigMap volume", resource.Kind, resource.Metadata.Name)
	}

	for _, container := range resource.Spec.Template.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if mount.MountPath != mountPath {
				continue
			}
			if mount.Name != volumeName || !mount.ReadOnly || mount.SubPath != "" {
				t.Errorf("%s %s container %s has invalid mapping source mount: %+v", resource.Kind, resource.Metadata.Name, container.Name, mount)
			}
			return true
		}
		t.Errorf("%s %s container %s has no mount at %s", resource.Kind, resource.Metadata.Name, containerName, mountPath)
		return false
	}
	t.Errorf("%s %s has no container named %s", resource.Kind, resource.Metadata.Name, containerName)
	return false
}

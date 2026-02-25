package flavors

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFlavors(t *testing.T) {
	// Create a temporary test flavors file
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "test-flavors.yaml")

	testYAML := `
community-el9:
  name: flightctl
  description: A helm chart for FlightControl
  buildImages:
    goToolset: registry.access.redhat.com/ubi9/go-toolset:1.24.6
    ubiMinimal: registry.access.redhat.com/ubi9/ubi-minimal:9.7
  images:
    api:
      image: quay.io/flightctl/flightctl-api
      tag: latest
    db:
      image: quay.io/sclorg/postgresql-16-c9s
      tag: "20250214"
  agentImages:
    osId: cs9-bootc
    enableCrb: false
  timeouts:
    db: 300s

community-el10:
  _inherit: community-el9
  buildImages:
    goToolset: registry.access.redhat.com/ubi10/go-toolset:1.24.6
    ubiMinimal: registry.access.redhat.com/ubi10/ubi-minimal:latest
  images:
    db:
      image: quay.io/sclorg/postgresql-16-c10s
  agentImages:
    osId: cs10-bootc
    enableCrb: true
`

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	flavors, err := LoadFlavors(flavorsFile, "")
	if err != nil {
		t.Fatalf("LoadFlavors failed: %v", err)
	}

	// Test that both flavors are loaded
	if len(flavors) != 2 {
		t.Errorf("Expected 2 flavors, got %d", len(flavors))
	}

	// Test community-el9 (parent)
	el9, exists := flavors["community-el9"]
	if !exists {
		t.Fatal("community-el9 flavor not found")
	}

	if el9.Name != "flightctl" {
		t.Errorf("Expected name 'flightctl', got '%s'", el9.Name)
	}

	if el9.BuildImages.GoToolset != "registry.access.redhat.com/ubi9/go-toolset:1.24.6" {
		t.Errorf("Unexpected goToolset for el9: %s", el9.BuildImages.GoToolset)
	}

	if el9.AgentImages.OsId != "cs9-bootc" {
		t.Errorf("Expected osId 'cs9-bootc', got '%s'", el9.AgentImages.OsId)
	}

	if el9.AgentImages.EnableCrb != false {
		t.Errorf("Expected EnableCrb false, got %v", el9.AgentImages.EnableCrb)
	}

	expectedTimeout := 300 * time.Second
	if el9.Timeouts.DB != expectedTimeout {
		t.Errorf("Expected timeout %v, got %v", expectedTimeout, el9.Timeouts.DB)
	}

	// Test community-el10 (child with inheritance)
	el10, exists := flavors["community-el10"]
	if !exists {
		t.Fatal("community-el10 flavor not found")
	}

	// Should inherit name from parent
	if el10.Name != "flightctl" {
		t.Errorf("Expected inherited name 'flightctl', got '%s'", el10.Name)
	}

	// Should have overridden goToolset
	if el10.BuildImages.GoToolset != "registry.access.redhat.com/ubi10/go-toolset:1.24.6" {
		t.Errorf("Unexpected goToolset for el10: %s", el10.BuildImages.GoToolset)
	}

	// Should inherit ubiMinimal but override with el10 value
	if el10.BuildImages.UbiMinimal != "registry.access.redhat.com/ubi10/ubi-minimal:latest" {
		t.Errorf("Unexpected ubiMinimal for el10: %s", el10.BuildImages.UbiMinimal)
	}

	// Should have overridden osId
	if el10.AgentImages.OsId != "cs10-bootc" {
		t.Errorf("Expected osId 'cs10-bootc', got '%s'", el10.AgentImages.OsId)
	}

	// Should have overridden EnableCrb
	if el10.AgentImages.EnableCrb != true {
		t.Errorf("Expected EnableCrb true, got %v", el10.AgentImages.EnableCrb)
	}

	// Should inherit timeout from parent
	if el10.Timeouts.DB != expectedTimeout {
		t.Errorf("Expected inherited timeout %v, got %v", expectedTimeout, el10.Timeouts.DB)
	}

	// Test image inheritance and override
	apiImage, apiTag, apiFound := el10.GetFlavorImageTag("api")
	if !apiFound {
		t.Error("API image should be inherited")
	}
	if apiImage != "quay.io/flightctl/flightctl-api" || apiTag != "latest" {
		t.Errorf("API image inheritance failed: %s:%s", apiImage, apiTag)
	}

	dbImage, dbTag, dbFound := el10.GetFlavorImageTag("db")
	if !dbFound {
		t.Error("DB image should exist")
	}
	if dbImage != "quay.io/sclorg/postgresql-16-c10s" {
		t.Errorf("DB image override failed: expected quay.io/sclorg/postgresql-16-c10s, got %s", dbImage)
	}
	if dbTag != "20250214" {
		t.Errorf("DB tag should be inherited: expected 20250214, got %s", dbTag)
	}
}

func TestGetFlavor(t *testing.T) {
	// Create a temporary test flavors file
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "test-flavors.yaml")

	testYAML := `
test-flavor:
  name: test
  description: Test flavor
  images:
    api:
      image: test/api
      tag: v1.0.0
`

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	flavor, err := GetFlavor("test-flavor", flavorsFile, "")
	if err != nil {
		t.Fatalf("GetFlavor failed: %v", err)
	}

	if flavor.Name != "test" {
		t.Errorf("Expected name 'test', got '%s'", flavor.Name)
	}

	// Test non-existent flavor
	_, err = GetFlavor("non-existent", flavorsFile, "")
	if err == nil {
		t.Error("Expected error for non-existent flavor")
	}
}

func TestListFlavors(t *testing.T) {
	// Create a temporary test flavors file
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "test-flavors.yaml")

	testYAML := `
flavor-a:
  name: A
flavor-b:
  name: B
flavor-c:
  name: C
`

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	names, err := ListFlavors(flavorsFile, "")
	if err != nil {
		t.Fatalf("ListFlavors failed: %v", err)
	}

	if len(names) != 3 {
		t.Errorf("Expected 3 flavors, got %d", len(names))
	}

	// Check that all expected names are present
	expectedNames := map[string]bool{
		"flavor-a": false,
		"flavor-b": false,
		"flavor-c": false,
	}

	for _, name := range names {
		if _, exists := expectedNames[name]; exists {
			expectedNames[name] = true
		} else {
			t.Errorf("Unexpected flavor name: %s", name)
		}
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("Expected flavor not found: %s", name)
		}
	}
}

func TestGetBuildImageReference(t *testing.T) {
	flavor := &FlavorConfig{
		BuildImages: BuildImagesConfig{
			GoToolset:  "registry.example.com/go:1.24",
			UbiMinimal: "registry.example.com/ubi:minimal",
			Base: BaseImageConfig{
				Image: "registry.example.com/base",
				Tag:   "latest",
				MinimalImage: ImageNameTag{
					Image: "registry.example.com/minimal",
					Tag:   "v1.0",
				},
			},
		},
	}

	tests := []struct {
		imageName   string
		expectedRef string
		expectError bool
	}{
		{"goToolset", "registry.example.com/go:1.24", false},
		{"ubiMinimal", "registry.example.com/ubi:minimal", false},
		{"base", "registry.example.com/base:latest", false},
		{"baseMinimal", "registry.example.com/minimal:v1.0", false},
		{"unknown", "", true},
	}

	for _, tt := range tests {
		ref, err := flavor.GetBuildImageReference(tt.imageName)
		if tt.expectError {
			if err == nil {
				t.Errorf("Expected error for image %s, but got none", tt.imageName)
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error for image %s: %v", tt.imageName, err)
			}
			if ref != tt.expectedRef {
				t.Errorf("For image %s, expected %s, got %s", tt.imageName, tt.expectedRef, ref)
			}
		}
	}
}

func TestGetFlavorImageTag(t *testing.T) {
	flavor := &FlavorConfig{
		Images: map[string]ImageConfig{
			"api": {
				Image: "quay.io/flightctl/api",
				Tag:   "v1.0.0",
			},
			"worker": {
				Image: "quay.io/flightctl/worker",
				// No tag specified
			},
		},
	}

	// Test existing image with tag
	image, tag, found := flavor.GetFlavorImageTag("api")
	if !found {
		t.Error("Expected to find api image")
	}
	if image != "quay.io/flightctl/api" {
		t.Errorf("Expected image 'quay.io/flightctl/api', got '%s'", image)
	}
	if tag != "v1.0.0" {
		t.Errorf("Expected tag 'v1.0.0', got '%s'", tag)
	}

	// Test existing image without tag
	image, tag, found = flavor.GetFlavorImageTag("worker")
	if !found {
		t.Error("Expected to find worker image")
	}
	if image != "quay.io/flightctl/worker" {
		t.Errorf("Expected image 'quay.io/flightctl/worker', got '%s'", image)
	}
	if tag != "" {
		t.Errorf("Expected empty tag, got '%s'", tag)
	}

	// Test non-existent image
	_, _, found = flavor.GetFlavorImageTag("non-existent")
	if found {
		t.Error("Expected not to find non-existent image")
	}
}

func TestInheritanceError(t *testing.T) {
	// Create a temporary test flavors file with circular inheritance
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "test-flavors.yaml")

	testYAML := `
flavor-a:
  _inherit: flavor-b
  name: A
flavor-b:
  _inherit: non-existent
  name: B
`

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	_, err = LoadFlavors(flavorsFile, "")
	if err == nil {
		t.Error("Expected error for missing parent flavor")
	}
}

func TestOverrideFunctionality(t *testing.T) {
	// Create a temporary test flavors file
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "test-flavors.yaml")
	overrideFile := filepath.Join(tempDir, "test-override.yaml")

	baseFlavorsYAML := `
simple-flavor:
  name: simple
  description: Simple flavor
  images:
    api:
      image: quay.io/simple/api
      tag: v1.0
    worker:
      image: quay.io/simple/worker
      tag: v1.0
`

	overrideFlavorsYAML := `
# Override simple-flavor - need to include all fields since YAML override replaces completely
simple-flavor:
  name: simple
  description: Simple flavor
  images:
    api:
      image: registry.example.com/custom/api
      tag: custom-v1.0
    worker:
      image: quay.io/simple/worker
      tag: v1.0
    db:
      image: registry.example.com/custom/db
      tag: latest

# Add new downstream flavor
downstream-flavor:
  name: downstream
  description: Downstream customized flavor
  images:
    worker:
      image: downstream.example.com/worker
      tag: special
`

	// Write test files
	err := os.WriteFile(flavorsFile, []byte(baseFlavorsYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavorsYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create test override file: %v", err)
	}

	// Test loading without override
	baseFlavors, err := LoadFlavors(flavorsFile, "")
	if err != nil {
		t.Fatalf("LoadFlavors without override failed: %v", err)
	}

	if len(baseFlavors) != 1 {
		t.Errorf("Expected 1 base flavor, got %d", len(baseFlavors))
	}

	// Test loading with override
	overriddenFlavors, err := LoadFlavors(flavorsFile, overrideFile)
	if err != nil {
		t.Fatalf("LoadFlavors with override failed: %v", err)
	}

	if len(overriddenFlavors) != 2 {
		t.Errorf("Expected 2 flavors with override, got %d", len(overriddenFlavors))
	}

	// Test override of existing flavor
	simpleFlavor, exists := overriddenFlavors["simple-flavor"]
	if !exists {
		t.Fatal("simple-flavor not found in overridden flavors")
	}

	// Check that API image was overridden
	apiImage, apiTag, found := simpleFlavor.GetFlavorImageTag("api")
	if !found {
		t.Error("API image not found in overridden simple-flavor")
	}
	if apiImage != "registry.example.com/custom/api" {
		t.Errorf("Expected overridden API image 'registry.example.com/custom/api', got '%s'", apiImage)
	}
	if apiTag != "custom-v1.0" {
		t.Errorf("Expected overridden API tag 'custom-v1.0', got '%s'", apiTag)
	}

	// Check that worker image still exists from original (override didn't specify it)
	workerImage, workerTag, found := simpleFlavor.GetFlavorImageTag("worker")
	if !found {
		t.Error("Worker image not found in overridden simple-flavor")
	}
	if workerImage != "quay.io/simple/worker" {
		t.Errorf("Expected preserved worker image 'quay.io/simple/worker', got '%s'", workerImage)
	}
	if workerTag != "v1.0" {
		t.Errorf("Expected preserved worker tag 'v1.0', got '%s'", workerTag)
	}

	// Check that new DB image was added by override
	dbImage, dbTag, found := simpleFlavor.GetFlavorImageTag("db")
	if !found {
		t.Error("DB image not found in overridden simple-flavor")
	}
	if dbImage != "registry.example.com/custom/db" {
		t.Errorf("Expected new DB image 'registry.example.com/custom/db', got '%s'", dbImage)
	}
	if dbTag != "latest" {
		t.Errorf("Expected new DB tag 'latest', got '%s'", dbTag)
	}

	// Test new downstream flavor
	downstreamFlavor, exists := overriddenFlavors["downstream-flavor"]
	if !exists {
		t.Fatal("downstream-flavor not found in overridden flavors")
	}

	if downstreamFlavor.Name != "downstream" {
		t.Errorf("Expected downstream name 'downstream', got '%s'", downstreamFlavor.Name)
	}

	// Downstream should have its own worker image
	downstreamWorker, downstreamWorkerTag, found := downstreamFlavor.GetFlavorImageTag("worker")
	if !found {
		t.Error("Worker image not found in downstream flavor")
	}
	if downstreamWorker != "downstream.example.com/worker" {
		t.Errorf("Expected downstream worker image 'downstream.example.com/worker', got '%s'", downstreamWorker)
	}
	if downstreamWorkerTag != "special" {
		t.Errorf("Expected downstream worker tag 'special', got '%s'", downstreamWorkerTag)
	}
}

func TestGetFlavorWithOverride(t *testing.T) {
	// Create test files
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "flavors.yaml")
	overrideFile := filepath.Join(tempDir, "override.yaml")

	baseFlavors := `
test-flavor:
  name: test
  images:
    api:
      image: quay.io/test/api
      tag: v1.0
`

	overrideFlavors := `
test-flavor:
  images:
    api:
      image: custom.example.com/api
      tag: v2.0
`

	err := os.WriteFile(flavorsFile, []byte(baseFlavors), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavors), 0644)
	if err != nil {
		t.Fatalf("Failed to create test override file: %v", err)
	}

	// Test without override
	flavor, err := GetFlavor("test-flavor", flavorsFile, "")
	if err != nil {
		t.Fatalf("GetFlavor without override failed: %v", err)
	}

	image, tag, found := flavor.GetFlavorImageTag("api")
	if !found {
		t.Error("API image not found in base flavor")
	}
	if image != "quay.io/test/api" || tag != "v1.0" {
		t.Errorf("Expected base API 'quay.io/test/api:v1.0', got '%s:%s'", image, tag)
	}

	// Test with override
	overriddenFlavor, err := GetFlavor("test-flavor", flavorsFile, overrideFile)
	if err != nil {
		t.Fatalf("GetFlavor with override failed: %v", err)
	}

	image, tag, found = overriddenFlavor.GetFlavorImageTag("api")
	if !found {
		t.Error("API image not found in overridden flavor")
	}
	if image != "custom.example.com/api" || tag != "v2.0" {
		t.Errorf("Expected overridden API 'custom.example.com/api:v2.0', got '%s:%s'", image, tag)
	}
}

func TestListFlavorsWithOverride(t *testing.T) {
	// Create test files
	tempDir := t.TempDir()
	flavorsFile := filepath.Join(tempDir, "flavors.yaml")
	overrideFile := filepath.Join(tempDir, "override.yaml")

	baseFlavors := `
flavor-a:
  name: A
flavor-b:
  name: B
`

	overrideFlavors := `
flavor-c:
  name: C
`

	err := os.WriteFile(flavorsFile, []byte(baseFlavors), 0644)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavors), 0644)
	if err != nil {
		t.Fatalf("Failed to create test override file: %v", err)
	}

	// Test without override
	baseNames, err := ListFlavors(flavorsFile, "")
	if err != nil {
		t.Fatalf("ListFlavors without override failed: %v", err)
	}

	if len(baseNames) != 2 {
		t.Errorf("Expected 2 base flavors, got %d", len(baseNames))
	}

	// Test with override
	overriddenNames, err := ListFlavors(flavorsFile, overrideFile)
	if err != nil {
		t.Fatalf("ListFlavors with override failed: %v", err)
	}

	if len(overriddenNames) != 3 {
		t.Errorf("Expected 3 flavors with override, got %d", len(overriddenNames))
	}

	// Check that flavor-c was added
	found := false
	for _, name := range overriddenNames {
		if name == "flavor-c" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Override flavor 'flavor-c' not found in overridden list")
	}
}

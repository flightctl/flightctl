package flavors

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0600)
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

	if el9.AgentImages.EnableCrb == nil || *el9.AgentImages.EnableCrb != false {
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
	if el10.AgentImages.EnableCrb == nil || *el10.AgentImages.EnableCrb != true {
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

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0600)
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

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0600)
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
  _inherit: flavor-a
  name: B
`

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0600)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	_, err = LoadFlavors(flavorsFile, "")
	if err == nil {
		t.Error("Expected error for circular inheritance")
	}
}

func TestMissingParentError(t *testing.T) {
	// Create a temporary test flavors file with missing parent flavor
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

	err := os.WriteFile(flavorsFile, []byte(testYAML), 0600)
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
	err := os.WriteFile(flavorsFile, []byte(baseFlavorsYAML), 0600)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavorsYAML), 0600)
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

	// Check that worker image remains unchanged (override specifies same worker image)
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

	err := os.WriteFile(flavorsFile, []byte(baseFlavors), 0600)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavors), 0600)
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

	err := os.WriteFile(flavorsFile, []byte(baseFlavors), 0600)
	if err != nil {
		t.Fatalf("Failed to create test flavors file: %v", err)
	}

	err = os.WriteFile(overrideFile, []byte(overrideFlavors), 0600)
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
	if !slices.Contains(overriddenNames, "flavor-c") {
		t.Error("Override flavor 'flavor-c' not found in overridden list")
	}
}

func TestDeepCopyInheritance(t *testing.T) {
	// Test that mergeFlavorConfigs performs deep copy of maps
	parent := &FlavorConfig{
		Name: "parent",
		Annotations: map[string]string{
			"parent-key": "parent-value",
		},
		Images: map[string]ImageConfig{
			"api": {
				Image: "parent/api",
				Tag:   "v1.0",
			},
		},
	}

	child := &FlavorConfig{
		Name: "child",
		Annotations: map[string]string{
			"child-key": "child-value",
		},
		Images: map[string]ImageConfig{
			"api": {
				Image: "child/api",
				Tag:   "v2.0",
			},
			"worker": {
				Image: "child/worker",
				Tag:   "v1.0",
			},
		},
	}

	// Store original parent values for comparison
	originalParentAnnotations := make(map[string]string)
	for k, v := range parent.Annotations {
		originalParentAnnotations[k] = v
	}
	originalParentImages := make(map[string]ImageConfig)
	for k, v := range parent.Images {
		originalParentImages[k] = v
	}

	// Merge child into parent
	result := mergeFlavorConfigs(parent, child)

	// Verify the result has correct merged values
	if result.Name != "child" {
		t.Errorf("Expected name 'child', got '%s'", result.Name)
	}

	// Check annotations are merged
	if result.Annotations["parent-key"] != "parent-value" {
		t.Error("Parent annotation not preserved")
	}
	if result.Annotations["child-key"] != "child-value" {
		t.Error("Child annotation not merged")
	}

	// Check images are merged
	if result.Images["api"].Image != "child/api" {
		t.Errorf("Expected merged API image 'child/api', got '%s'", result.Images["api"].Image)
	}
	if result.Images["worker"].Image != "child/worker" {
		t.Error("Child worker image not added")
	}

	// CRITICAL TEST: Verify parent maps are not modified (deep copy worked)
	if len(parent.Annotations) != len(originalParentAnnotations) {
		t.Error("Parent annotations map was modified (should be deep copied)")
	}
	for k, v := range originalParentAnnotations {
		if parent.Annotations[k] != v {
			t.Errorf("Parent annotation '%s' was modified from '%s' to '%s'", k, v, parent.Annotations[k])
		}
	}

	if len(parent.Images) != len(originalParentImages) {
		t.Error("Parent images map was modified (should be deep copied)")
	}
	for k, v := range originalParentImages {
		if parent.Images[k] != v {
			t.Errorf("Parent image '%s' was modified", k)
		}
	}

	// Verify child key not in parent
	if _, exists := parent.Annotations["child-key"]; exists {
		t.Error("Child annotation was added to parent map (deep copy failed)")
	}
	if _, exists := parent.Images["worker"]; exists {
		t.Error("Child image was added to parent map (deep copy failed)")
	}
}

func TestPointerBooleanInheritance(t *testing.T) {
	// Test that pointer boolean fields properly handle inheritance
	// Parent has EnableCrb=true, EpelNext=false
	enableCrbTrue := true
	epelNextFalse := false
	parent := &FlavorConfig{
		Name: "parent",
		AgentImages: AgentImagesConfig{
			OsId:      "parent-os",
			EnableCrb: &enableCrbTrue,
			EpelNext:  &epelNextFalse,
		},
	}

	// Test 1: Child explicitly sets EnableCrb=false, omits EpelNext (should inherit)
	enableCrbFalse := false
	child1 := &FlavorConfig{
		Name: "child1",
		AgentImages: AgentImagesConfig{
			EnableCrb: &enableCrbFalse, // Explicitly set to false
			// EpelNext is nil - should inherit from parent
		},
	}

	result1 := mergeFlavorConfigs(parent, child1)

	// Verify EnableCrb was overridden to false
	if result1.AgentImages.EnableCrb == nil || *result1.AgentImages.EnableCrb != false {
		t.Errorf("Expected EnableCrb to be explicitly false, got %v", result1.AgentImages.EnableCrb)
	}

	// Verify EpelNext was inherited from parent (false)
	if result1.AgentImages.EpelNext == nil || *result1.AgentImages.EpelNext != false {
		t.Errorf("Expected EpelNext to be inherited as false, got %v", result1.AgentImages.EpelNext)
	}

	// Test 2: Child omits both fields (should inherit both)
	child2 := &FlavorConfig{
		Name: "child2",
		AgentImages: AgentImagesConfig{
			OsId: "child-os",
			// Both EnableCrb and EpelNext are nil - should inherit from parent
		},
	}

	result2 := mergeFlavorConfigs(parent, child2)

	// Verify both fields were inherited from parent
	if result2.AgentImages.EnableCrb == nil || *result2.AgentImages.EnableCrb != true {
		t.Errorf("Expected EnableCrb to be inherited as true, got %v", result2.AgentImages.EnableCrb)
	}
	if result2.AgentImages.EpelNext == nil || *result2.AgentImages.EpelNext != false {
		t.Errorf("Expected EpelNext to be inherited as false, got %v", result2.AgentImages.EpelNext)
	}

	// Verify OsId was overridden
	if result2.AgentImages.OsId != "child-os" {
		t.Errorf("Expected OsId to be overridden to 'child-os', got '%s'", result2.AgentImages.OsId)
	}

	// Test 3: Child explicitly sets both fields
	epelNextTrue := true
	child3 := &FlavorConfig{
		Name: "child3",
		AgentImages: AgentImagesConfig{
			EnableCrb: &enableCrbFalse, // Explicitly false
			EpelNext:  &epelNextTrue,   // Explicitly true
		},
	}

	result3 := mergeFlavorConfigs(parent, child3)

	// Verify both fields were explicitly set by child
	if result3.AgentImages.EnableCrb == nil || *result3.AgentImages.EnableCrb != false {
		t.Errorf("Expected EnableCrb to be explicitly false, got %v", result3.AgentImages.EnableCrb)
	}
	if result3.AgentImages.EpelNext == nil || *result3.AgentImages.EpelNext != true {
		t.Errorf("Expected EpelNext to be explicitly true, got %v", result3.AgentImages.EpelNext)
	}
}

func TestNilFlavorEntryHandling(t *testing.T) {
	// Test that nil flavor entries are properly detected and rejected

	// Create a FlavorsMap with a nil entry
	rawFlavors := make(map[string]*FlavorConfigRaw)
	rawFlavors["valid-flavor"] = &FlavorConfigRaw{
		FlavorConfig: FlavorConfig{
			Name: "Valid Flavor",
		},
	}
	rawFlavors["nil-flavor"] = nil // This should cause an error

	// Test processFlavorInheritance directly with nil rawFlavor
	_, err := processFlavorInheritance("nil-test", nil, rawFlavors)
	if err == nil {
		t.Error("Expected error when processFlavorInheritance called with nil rawFlavor")
	}
	expectedMsg := "flavor nil-test has nil configuration"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("Expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}

	// Test with nil parent flavor
	rawFlavorsWithNilParent := make(map[string]*FlavorConfigRaw)
	rawFlavorsWithNilParent["child"] = &FlavorConfigRaw{
		Inherit: "nil-parent",
		FlavorConfig: FlavorConfig{
			Name: "Child Flavor",
		},
	}
	rawFlavorsWithNilParent["nil-parent"] = nil

	_, err = processFlavorInheritance("child", rawFlavorsWithNilParent["child"], rawFlavorsWithNilParent)
	if err == nil {
		t.Error("Expected error when parent flavor is nil")
	}
	expectedParentMsg := "parent flavor nil-parent has nil configuration (referenced by flavor child)"
	if !strings.Contains(err.Error(), expectedParentMsg) {
		t.Errorf("Expected error message to contain '%s', got '%s'", expectedParentMsg, err.Error())
	}
}

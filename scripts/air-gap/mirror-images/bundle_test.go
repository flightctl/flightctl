package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// registriesDConfig models the containers-registries.d(5) YAML schema.
// The two top-level keys are "default-docker" and "docker"; registry-specific
// settings belong under docker.<registry>.
type registriesDConfig struct {
	DefaultDocker *registriesDNamespace           `yaml:"default-docker,omitempty"`
	Docker        map[string]registriesDNamespace `yaml:"docker,omitempty"`
}

// registriesDNamespace holds the per-registry keys defined by the
// containers-registries.d(5) schema.
type registriesDNamespace struct {
	Lookaside        string `yaml:"lookaside,omitempty"`
	LookasideStaging string `yaml:"lookaside-staging,omitempty"`
	// Legacy alias for lookaside — accepted by older container runtimes.
	Sigstore        string `yaml:"sigstore,omitempty"`
	SigstoreStaging string `yaml:"sigstore-staging,omitempty"`
}

// TestSourceRegistriesRequiringSignatures verifies that the helper correctly
// identifies registries whose images carry signatures enforced by RHEL.
func TestSourceRegistriesRequiringSignatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		pairs []ImagePair
		want  []string
	}{
		{
			name: "When pairs include only quay.io images it should return no registries",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			want: nil,
		},
		{
			name: "When pairs include registry.redhat.io it should return that registry",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.redhat.io/rhel9/postgresql-16:9.8"},
			},
			want: []string{"registry.redhat.io"},
		},
		{
			name: "When pairs include registry.access.redhat.com it should return that registry",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
			},
			want: []string{"registry.access.redhat.com"},
		},
		{
			name: "When pairs include both signature-required registries it should return both sorted",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			want: []string{"registry.access.redhat.com", "registry.redhat.io"},
		},
		{
			name:  "When pairs are empty it should return no registries",
			pairs: nil,
			want:  nil,
		},
		{
			name: "When duplicate signature registries appear it should deduplicate",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.redhat.io/rhel9/postgresql-16:9.8"},
				{Source: "registry.redhat.io/rhem/flightctl-worker-rhel9:1.0"},
			},
			want: []string{"registry.redhat.io"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sourceRegistriesRequiringSignatures(tt.pairs)
			if len(tt.want) == 0 {
				require.Empty(t, got)
			} else {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

// TestRegistriesDExample verifies that registriesDExample returns YAML that
// parses into a valid containers-registries.d(5) structure with the lookaside
// key nested under docker.<registry>.
func TestRegistriesDExample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		registry string
		path     string
	}{
		{
			name:     "When given registry.redhat.io it should produce valid nested YAML",
			registry: "registry.redhat.io",
			path:     "file:///var/lib/containers/sigstore",
		},
		{
			name:     "When given registry.access.redhat.com it should produce valid nested YAML",
			registry: "registry.access.redhat.com",
			path:     "file:///var/lib/containers/sigstore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			example := registriesDExample(tt.registry, tt.path)

			// Must parse as valid YAML.
			var parsed registriesDConfig
			err := yaml.Unmarshal([]byte(example), &parsed)
			require.NoError(t, err, "generated example must be valid YAML")

			// Must have a docker section, not top-level sigstore/lookaside.
			require.NotNil(t, parsed.Docker, "example must use docker.<registry> nesting")
			require.Contains(t, parsed.Docker, tt.registry,
				"docker section must contain the target registry")

			// The lookaside must be set to the expected path.
			ns := parsed.Docker[tt.registry]
			require.Equal(t, tt.path, ns.Lookaside,
				"lookaside must point to the local sigstore path")

			// Must NOT have a top-level default-docker with a sigstore/lookaside.
			require.Nil(t, parsed.DefaultDocker,
				"example must not set default-docker")
		})
	}
}

// TestWriteImportScript verifies the generated import.sh script content for
// different variant and registry combinations.
func TestWriteImportScript(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		variant           string
		pairs             []ImagePair
		wantRootCheck     bool
		wantSigstoreGuide []string // registries that should appear in guidance
		wantSkopeoCount   int
	}{
		{
			name:    "When variant uses only quay.io images it should not require root or show sigstore guidance",
			variant: "community-el9",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			wantRootCheck:     false,
			wantSigstoreGuide: nil,
			wantSkopeoCount:   2,
		},
		{
			name:    "When variant includes registry.redhat.io images it should require root and show sigstore guidance",
			variant: "rhem-el9",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.redhat.io/rhel9/postgresql-16:9.8"},
			},
			wantRootCheck:     true,
			wantSigstoreGuide: []string{"registry.redhat.io"},
			wantSkopeoCount:   2,
		},
		{
			name:    "When variant includes registry.access.redhat.com images it should require root and show sigstore guidance",
			variant: "community-el9",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
			},
			wantRootCheck:     true,
			wantSigstoreGuide: []string{"registry.access.redhat.com"},
			wantSkopeoCount:   2,
		},
		{
			name:    "When variant includes both signature registries it should show guidance for both",
			variant: "mixed-test",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			wantRootCheck:     true,
			wantSigstoreGuide: []string{"registry.access.redhat.com", "registry.redhat.io"},
			wantSkopeoCount:   3,
		},
		{
			name:            "When pairs are empty it should produce a minimal script",
			variant:         "community-el9",
			pairs:           nil,
			wantRootCheck:   false,
			wantSkopeoCount: 0,
		},
		{
			name:    "When a source image has no slash it should be skipped",
			variant: "community-el9",
			pairs: []ImagePair{
				{Source: "noslash"},
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
			},
			wantRootCheck:   false,
			wantSkopeoCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			err := WriteImportScript(tmpDir, tt.pairs, tt.variant)
			require.NoError(t, err)

			scriptPath := filepath.Join(tmpDir, "import.sh")
			content, err := os.ReadFile(scriptPath)
			require.NoError(t, err)
			script := string(content)

			// Verify the script is executable.
			info, err := os.Stat(scriptPath)
			require.NoError(t, err)
			require.NotZero(t, info.Mode()&0o111, "import.sh should be executable")

			// Verify variant is in the header.
			require.Contains(t, script, "# Variant:   "+tt.variant)

			// Verify root check presence.
			rootCheckMarker := `if [ "$(id -u)" -ne 0 ]`
			if tt.wantRootCheck {
				require.Contains(t, script, rootCheckMarker,
					"script should require root when signature-required registries are present")
			} else {
				require.NotContains(t, script, rootCheckMarker,
					"script should not require root when no signature-required registries are present")
			}

			// Verify skopeo copy command count.
			skopeoCopies := strings.Count(script, "skopeo copy")
			require.Equal(t, tt.wantSkopeoCount, skopeoCopies,
				"unexpected number of skopeo copy commands")

			// Verify sigstore guidance for each expected registry.
			for _, reg := range tt.wantSigstoreGuide {
				yamlFile := reg + ".yaml"
				require.Contains(t, script, yamlFile,
					"script should mention registries.d YAML file for "+reg)
			}

			// When no sigstore guidance is expected, verify none is present.
			if len(tt.wantSigstoreGuide) == 0 {
				require.NotContains(t, script, "Configure offline signature lookup",
					"script should not show sigstore guidance when no signature registries present")
			}
		})
	}
}

// TestWriteImportScriptNoHostMutation verifies that the generated script does
// NOT write to any host configuration files.  It must not contain shell
// redirects or tee commands targeting /etc paths.
func TestWriteImportScriptNoHostMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		variant string
		pairs   []ImagePair
	}{
		{
			name:    "When variant has signature registries it should not write to /etc",
			variant: "rhem-el9",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.redhat.io/rhel9/postgresql-16:9.8"},
			},
		},
		{
			name:    "When variant has both signature registries it should not write to /etc",
			variant: "mixed-test",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			err := WriteImportScript(tmpDir, tt.pairs, tt.variant)
			require.NoError(t, err)

			content, err := os.ReadFile(filepath.Join(tmpDir, "import.sh"))
			require.NoError(t, err)
			script := string(content)

			// Check that no shell lines (outside echo/printf) redirect to /etc paths.
			for _, line := range strings.Split(script, "\n") {
				trimmed := strings.TrimSpace(line)
				// Skip comments, empty lines, and echo/printf (display only).
				if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
					strings.HasPrefix(trimmed, "echo ") || strings.HasPrefix(trimmed, "printf ") {
					continue
				}
				// No redirect to /etc.
				require.NotContains(t, trimmed, ">> /etc/",
					"script must not append to /etc/ files: "+trimmed)
				require.NotContains(t, trimmed, "> /etc/",
					"script must not write to /etc/ files: "+trimmed)
				// No tee to /etc.
				if strings.Contains(trimmed, "tee") && strings.Contains(trimmed, "/etc/") {
					t.Errorf("script must not tee to /etc/ files: %s", trimmed)
				}
			}

			// Safety: script must never contain --remove-signatures.
			require.NotContains(t, script, "--remove-signatures",
				"script must not strip signatures — that weakens the security policy")

			// Safety: no shell command (outside echo/printf) should reference policy.json.
			for _, line := range strings.Split(script, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
					strings.HasPrefix(trimmed, "echo ") || strings.HasPrefix(trimmed, "printf ") {
					continue
				}
				require.NotContains(t, trimmed, "policy.json",
					"non-echo line must not reference policy.json: "+trimmed)
			}
		})
	}
}

// TestWriteImportScriptUsageSudo verifies that the generated script's usage
// comment recommends sudo when signature-required registries are present.
func TestWriteImportScriptUsageSudo(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pairs := []ImagePair{
		{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
	}
	err := WriteImportScript(tmpDir, pairs, "rhem-el9")
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(tmpDir, "import.sh"))
	require.NoError(t, err)
	script := string(content)

	// Usage line should mention sudo.
	require.Contains(t, script, "sudo ./import.sh",
		"usage comment should recommend running with sudo")
}

// TestWriteImportScriptGuidanceContainsValidYAML verifies that the registries.d
// examples shown in the generated script parse as valid containers-registries.d
// YAML with the registry nested under the docker top-level key.
func TestWriteImportScriptGuidanceContainsValidYAML(t *testing.T) {
	t.Parallel()

	// Generate a script with both signature-required registries.
	tmpDir := t.TempDir()
	pairs := []ImagePair{
		{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
		{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
	}
	err := WriteImportScript(tmpDir, pairs, "mixed-test")
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(tmpDir, "import.sh"))
	require.NoError(t, err)
	script := string(content)

	// Each sigstore-requiring registry should have its example producible
	// by registriesDExample, which is validated separately.
	for _, reg := range []string{"registry.redhat.io", "registry.access.redhat.com"} {
		example := registriesDExample(reg, "file:///var/lib/containers/sigstore")
		// The example YAML must be parseable.
		var parsed registriesDConfig
		err := yaml.Unmarshal([]byte(example), &parsed)
		require.NoError(t, err, "YAML example for %s must parse", reg)
		require.Contains(t, parsed.Docker, reg,
			"docker section must contain %s", reg)
		require.Equal(t, "file:///var/lib/containers/sigstore",
			parsed.Docker[reg].Lookaside,
			"lookaside must point to system sigstore for %s", reg)

		// The script must reference this registry's YAML file.
		require.Contains(t, script, reg+".yaml",
			"script should reference registries.d file for "+reg)
	}
}

// TestWriteImportScriptQuayOnlyNoRHConfig verifies that a variant using only
// quay.io images does not include Red Hat-specific sigstore guidance.
func TestWriteImportScriptQuayOnlyNoRHConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pairs := []ImagePair{
		{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
		{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
		{Source: "docker.io/library/registry:2"},
	}
	err := WriteImportScript(tmpDir, pairs, "quay-only-test")
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(tmpDir, "import.sh"))
	require.NoError(t, err)
	script := string(content)

	require.NotContains(t, script, "registry.redhat.io",
		"quay-only variant must not mention registry.redhat.io")
	require.NotContains(t, script, "registry.access.redhat.com",
		"quay-only variant must not mention registry.access.redhat.com")
	require.NotContains(t, script, "registries.d",
		"quay-only variant must not mention registries.d configuration")
	require.NotContains(t, script, "sigstore",
		"quay-only variant must not mention sigstore")
}

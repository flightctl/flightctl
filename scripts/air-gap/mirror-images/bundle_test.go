package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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

// TestWriteImportScript verifies the generated import.sh script content for
// different variant and registry combinations.
func TestWriteImportScript(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		variant            string
		pairs              []ImagePair
		wantRootCheck      bool
		wantSigstoreConfig []string // registries.d filenames that should be configured
		wantSkopeoCount    int
	}{
		{
			name:    "When variant uses only quay.io images it should not require root or configure sigstore",
			variant: "community-el9",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			wantRootCheck:      false,
			wantSigstoreConfig: nil,
			wantSkopeoCount:    2,
		},
		{
			name:    "When variant includes registry.redhat.io images it should require root and configure sigstore",
			variant: "rhem-el9",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.redhat.io/rhel9/postgresql-16:9.8"},
			},
			wantRootCheck:      true,
			wantSigstoreConfig: []string{"registry.redhat.io.yaml"},
			wantSkopeoCount:    2,
		},
		{
			name:    "When variant includes registry.access.redhat.com images it should require root and configure sigstore",
			variant: "community-el9",
			pairs: []ImagePair{
				{Source: "quay.io/flightctl/flightctl-api-el9:latest"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
			},
			wantRootCheck:      true,
			wantSigstoreConfig: []string{"registry.access.redhat.com.yaml"},
			wantSkopeoCount:    2,
		},
		{
			name:    "When variant includes both signature registries it should configure both",
			variant: "mixed-test",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			wantRootCheck:      true,
			wantSigstoreConfig: []string{"registry.access.redhat.com.yaml", "registry.redhat.io.yaml"},
			wantSkopeoCount:    3,
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

			// Verify sigstore configuration blocks.
			for _, yamlFile := range tt.wantSigstoreConfig {
				require.Contains(t, script, yamlFile,
					"script should configure sigstore for "+yamlFile)
				require.Contains(t, script, "sigstore:.*file:///var/lib/containers/sigstore",
					"script should reference the system sigstore directory")
			}

			// Safety: script must never contain --remove-signatures.
			require.NotContains(t, script, "--remove-signatures",
				"script must not strip signatures — that weakens the security policy")

			// Safety: script must never modify policy.json.
			require.NotContains(t, script, "policy.json",
				"script must not modify the container signature policy")
		})
	}
}

// TestWriteImportScriptSigstorePreservesExisting verifies that the generated
// sigstore configuration logic does not blindly overwrite existing files — it
// appends only when the setting is not already present.
func TestWriteImportScriptSigstorePreservesExisting(t *testing.T) {
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

	// The script should check whether the sigstore line already exists
	// before appending, using grep.
	require.Contains(t, script, "grep -q",
		"sigstore config should check for existing setting before appending")

	// The sigstore configuration section should only use >> (append), never
	// a bare > (overwrite) as a shell redirect to the registries.d file.
	// Extract lines that write to $REGFILE, excluding echo-quoted hints shown
	// to the user (which may contain "> $REGFILE" as instructional text inside
	// a quoted string).
	sigstoreSection := script[strings.Index(script, "Configure signature lookup"):]
	for _, line := range strings.Split(sigstoreSection, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip echo/printf lines — they only print advice, not write files.
		if strings.HasPrefix(trimmed, "echo ") || strings.HasPrefix(trimmed, "printf ") {
			continue
		}
		// Any remaining line that redirects to $REGFILE must use >>.
		if strings.Contains(trimmed, "> \"$REGFILE\"") || strings.Contains(trimmed, "> $REGFILE") {
			require.Contains(t, trimmed, ">>",
				"sigstore config must append (>>), not overwrite (>): line: "+trimmed)
		}
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

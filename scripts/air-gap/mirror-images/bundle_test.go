package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// key nested under docker.<destination>.  After a registries.conf redirect,
// podman resolves the lookaside by the physical destination, so the key must
// match the import registry — not the original source.
func TestRegistriesDExample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		destination string
		path        string
	}{
		{
			name:        "When given default localhost:5000 it should produce valid nested YAML",
			destination: "localhost:5000",
			path:        "file:///var/lib/containers/sigstore",
		},
		{
			name:        "When given a custom host:port it should produce valid nested YAML",
			destination: "registry.internal.example.com:8443",
			path:        "file:///var/lib/containers/sigstore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			example := registriesDExample(tt.destination, tt.path)

			// Must parse as valid YAML.
			var parsed registriesDConfig
			err := yaml.Unmarshal([]byte(example), &parsed)
			require.NoError(t, err, "generated example must be valid YAML")

			// Must have a docker section with destination key, not source registries.
			require.NotNil(t, parsed.Docker, "example must use docker.<destination> nesting")
			require.Contains(t, parsed.Docker, tt.destination,
				"docker section must contain the import destination")

			// The lookaside must be set to the expected path.
			ns := parsed.Docker[tt.destination]
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
		wantSigstoreGuide bool // whether destination-scoped guidance should appear
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
			wantSigstoreGuide: false,
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
			wantSigstoreGuide: true,
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
			wantSigstoreGuide: true,
			wantSkopeoCount:   2,
		},
		{
			name:    "When variant includes both signature registries it should show a single destination-scoped block",
			variant: "mixed-test",
			pairs: []ImagePair{
				{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
				{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
				{Source: "quay.io/flightctl/flightctl-worker-el9:latest"},
			},
			wantRootCheck:     true,
			wantSigstoreGuide: true,
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

			// Verify destination-scoped sigstore guidance.
			if tt.wantSigstoreGuide {
				// Guidance must reference $REGISTRY (destination scope), not
				// individual source registry names as YAML keys.
				// The key is quoted for safe host:port/namespace values.
				require.Contains(t, script, `\"$REGISTRY\":`,
					"guidance must reference quoted $REGISTRY as the YAML key")
				require.Contains(t, script, "lookaside: file:///var/lib/containers/sigstore",
					"guidance must include the default sigstore path")
				// Must be a single docker: block — count echo lines with "docker:"
				dockerEchoCount := strings.Count(script, `echo "  docker:"`)
				require.Equal(t, 1, dockerEchoCount,
					"guidance must show exactly one destination-scoped docker: block")
				// Filename must be flat and fixed, not derived from destination.
				require.Contains(t, script, "flightctl-offline.yaml",
					"guidance must use a fixed flat filename")
				// Source registry names must NOT appear as YAML file references.
				require.NotContains(t, script, "registry.redhat.io.yaml",
					"guidance must not reference source registry YAML files")
				require.NotContains(t, script, "registry.access.redhat.com.yaml",
					"guidance must not reference source registry YAML files")
				// Merge/preserve/no-duplicate instructions must be present.
				require.Contains(t, script, "merge the lookaside key",
					"guidance must include merge instructions")
				require.Contains(t, script, "Do not duplicate",
					"guidance must warn against duplicate namespaces")
			}

			// When no sigstore guidance is expected, verify none is present.
			if !tt.wantSigstoreGuide {
				require.NotContains(t, script, "Signature configuration",
					"script should not show sigstore guidance when no signature registries present")
			}

			// Verify args are parsed BEFORE the root guard (if present).
			if tt.wantRootCheck {
				argsPos := strings.Index(script, `REGISTRY="localhost:5000"`)
				guardPos := strings.Index(script, rootCheckMarker)
				require.Greater(t, guardPos, argsPos,
					"argument parsing must appear before the root guard")
			}
		})
	}
}

// hostWritePattern matches shell output redirection (>> or >) targeting /etc
// or a variable that could resolve to an /etc path (e.g. $REGFILE, $REGISTRIES_D).
// It is intentionally pattern-based, not a full shell parser.  It catches the
// specific regression from the original echo-append approach while allowing
// echo lines that merely mention /etc paths as instructional text (without
// a redirect operator).
//
// Matches: echo "..." >> /etc/..., echo "..." >> "$REGFILE", tee /etc/...,
//
//	echo "..." > "$REGISTRIES_D/...", printf ... >> /etc/...
//
// Does not match: echo "/etc/containers/registries.d/..." (display only),
//
//	echo "..." >&2 (stderr diagnostic)
var hostWritePattern = regexp.MustCompile(
	`(?:>>?\s*/etc/)|` + // redirect to literal /etc path
		`(?:>>?\s*"\$(?:REGFILE|REGISTRIES_D)["/])|` + // redirect to config variable
		`(?:>>?\s*\$(?:REGFILE|REGISTRIES_D)["/\s])|` + // unquoted variable redirect
		`(?:tee\s+(?:-a\s+)?(?:/etc/|"\$(?:REGFILE|REGISTRIES_D)))`, // tee to config path
)

// TestWriteImportScriptNoHostMutation verifies that the generated script does
// NOT write to any host configuration files.  Unlike the previous version
// which skipped echo/printf lines entirely (missing the echo >> regression),
// this checks ALL lines including echo and printf for output redirection to
// host paths.  Lines that merely display /etc paths as instructional text
// (without redirect operators) are allowed.
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

			// Check ALL lines (including echo/printf) for host-file writes.
			for i, line := range strings.Split(script, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if hostWritePattern.MatchString(trimmed) {
					t.Errorf("line %d writes to host config path: %s", i+1, trimmed)
				}
			}

			// Safety: script must never contain --remove-signatures.
			require.NotContains(t, script, "--remove-signatures",
				"script must not strip signatures — that weakens the security policy")

			// Safety: no non-display line should operate on policy.json.
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

// TestHostWritePatternCatchesRegressions verifies that the hostWritePattern
// correctly identifies the echo-append pattern from the original implementation
// (which wrote to host registries.d files) and rejects similar patterns using
// config-path variables, while allowing display-only mentions of /etc paths.
func TestHostWritePatternCatchesRegressions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		wantMatch bool
	}{
		// Must match: original regression patterns
		{
			name:      "When echo appends to /etc it should match",
			line:      `echo "sigstore: file:///var/lib/containers/sigstore" >> /etc/containers/registries.d/registry.redhat.io.yaml`,
			wantMatch: true,
		},
		{
			name:      "When echo appends to REGFILE variable it should match",
			line:      `echo "sigstore: file://$SIGSTORE_DIR" >> "$REGFILE"`,
			wantMatch: true,
		},
		{
			name:      "When echo writes to REGISTRIES_D variable it should match",
			line:      `echo "content" > "$REGISTRIES_D/registry.redhat.io.yaml"`,
			wantMatch: true,
		},
		{
			name:      "When tee appends to /etc it should match",
			line:      `echo "sigstore: ..." | tee -a /etc/containers/registries.d/registry.redhat.io.yaml`,
			wantMatch: true,
		},
		{
			name:      "When tee writes to REGFILE variable it should match",
			line:      `echo "..." | tee "$REGFILE"`,
			wantMatch: true,
		},
		// Must NOT match: display-only mentions
		{
			name:      "When echo displays /etc path without redirect it should not match",
			line:      `echo "  File: /etc/containers/registries.d/registry.redhat.io.yaml"`,
			wantMatch: false,
		},
		{
			name:      "When echo writes to stderr it should not match",
			line:      `echo "Error: something went wrong" >&2`,
			wantMatch: false,
		},
		{
			name:      "When echo mentions docker section it should not match",
			line:      `echo "  Merge into the existing docker section:"`,
			wantMatch: false,
		},
		{
			name:      "When a comment mentions /etc it should not match",
			line:      `# configure /etc/containers/registries.d/`,
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hostWritePattern.MatchString(tt.line)
			require.Equal(t, tt.wantMatch, got,
				"hostWritePattern.MatchString(%q) = %v, want %v", tt.line, got, tt.wantMatch)
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

// extractDestinationEchoYAML extracts the destination-scoped YAML content
// from the generated script's echo lines.  It looks for the echo "  docker:"
// line that starts the YAML block, then collects subsequent echo lines
// (destination key, lookaside) until a blank echo following content.
func extractDestinationEchoYAML(script string) string {
	lines := strings.Split(script, "\n")
	var collecting bool
	var yamlLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Find the docker: echo line that starts the YAML block.
		if !collecting && trimmed == `echo "  docker:"` {
			collecting = true
			yamlLines = append(yamlLines, "docker:")
			continue
		}
		if collecting {
			// A blank echo or non-echo line after we've collected YAML ends the block.
			if trimmed == `echo ""` || trimmed == "" || !strings.HasPrefix(trimmed, "echo ") {
				if len(yamlLines) > 1 {
					break
				}
				continue
			}
			// Extract payload from echo "..." — remove echo prefix, quotes,
			// and the display indentation added by the template.
			payload := trimmed[len(`echo "`):]
			payload = strings.TrimSuffix(payload, `"`)
			// Unescape bash double-quote escapes (\" → ") so the extracted
			// YAML contains real quotes for the destination key.
			payload = strings.ReplaceAll(payload, `\"`, `"`)
			// Remove display indent (2 spaces from the echo indentation).
			payload = strings.TrimPrefix(payload, "  ")
			if strings.TrimSpace(payload) != "" {
				yamlLines = append(yamlLines, payload)
			}
		}
	}
	return strings.Join(yamlLines, "\n") + "\n"
}

// TestWriteImportScriptGuidanceContainsValidYAML extracts the ACTUAL echo
// payloads from the generated script and validates that they form a valid
// containers-registries.d(5) YAML structure with the lookaside nested under
// docker.<destination>.  The destination is $REGISTRY (a bash variable),
// which appears literally in the extracted YAML.  It also runs bash -n to
// verify the generated script has valid shell syntax.
func TestWriteImportScriptGuidanceContainsValidYAML(t *testing.T) {
	t.Parallel()

	// Generate a script with both signature-required registries.
	// Even with multiple sources, there should be a single destination block.
	tmpDir := t.TempDir()
	pairs := []ImagePair{
		{Source: "registry.redhat.io/rhem/flightctl-api-rhel9:1.0"},
		{Source: "registry.access.redhat.com/ubi9/ubi-minimal:9.8"},
	}
	err := WriteImportScript(tmpDir, pairs, "mixed-test")
	require.NoError(t, err)

	scriptPath := filepath.Join(tmpDir, "import.sh")
	content, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	script := string(content)

	// Validate shell syntax with bash -n (syntax check only, no execution).
	cmd := exec.Command("bash", "-n", scriptPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bash -n syntax check failed: %s", string(out))

	// Extract and validate the single destination-scoped YAML block.
	// The key is "$REGISTRY" (a bash variable, literal in the script text).
	extractedYAML := extractDestinationEchoYAML(script)
	require.NotEqual(t, "\n", extractedYAML,
		"should find destination-scoped YAML guidance in the generated script")

	var parsed registriesDConfig
	err = yaml.Unmarshal([]byte(extractedYAML), &parsed)
	require.NoError(t, err,
		"extracted YAML must parse; got:\n%s", extractedYAML)
	require.NotNil(t, parsed.Docker,
		"extracted YAML must have docker section")
	// The key is "$REGISTRY" literally (bash variable, not yet expanded).
	require.Contains(t, parsed.Docker, "$REGISTRY",
		"docker section must contain $REGISTRY as the destination key")
	require.Equal(t, "file:///var/lib/containers/sigstore",
		parsed.Docker["$REGISTRY"].Lookaside,
		"lookaside must point to the default sigstore path")
	// Must be exactly one entry — destination scope, not per-source.
	require.Len(t, parsed.Docker, 1,
		"guidance must contain exactly one docker entry (destination-scoped)")
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

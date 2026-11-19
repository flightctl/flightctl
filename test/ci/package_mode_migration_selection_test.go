package ci_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	packagemodemigration "github.com/flightctl/flightctl/test/e2e/package_mode_migration"
	"github.com/stretchr/testify/require"
)

// The mixed OS-mode migration suite needs the package-mode agent image, the
// same way the package-mode suite does, and it mutates deployment-wide state.
// Two decisions therefore have to hold and neither lives in Go: the e2e runner
// must preload that image whenever the suite is selected, and a run that picks
// the suite up by wildcard must not activate it. These tests exercise the real
// script and the real gate, because nothing else does.

const preloadFunc = "should_preload_package_mode_image"

// runE2EScriptPath is the real runner, relative to this package.
func runE2EScriptPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "scripts", "run_e2e_tests.sh")
	_, err := os.Stat(path)
	require.NoError(t, err, "e2e runner script must exist at %s", path)
	return path
}

// extractShellFunc returns the text of a shell function from a script, so the
// test runs the repository's own logic rather than a copy of it.
func extractShellFunc(t *testing.T, scriptPath, name string) string {
	t.Helper()
	raw, err := os.ReadFile(scriptPath) //nolint:gosec // G304: in-repo script at a fixed relative path
	require.NoError(t, err)

	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), name+"()") {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "function %s not found in %s", name, scriptPath)

	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("function %s in %s is not terminated", name, scriptPath)
	return ""
}

// preloads reports what the real function decides for a given GO_E2E_DIRS.
func preloads(t *testing.T, funcText string, dirs ...string) bool {
	t.Helper()

	quoted := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		quoted = append(quoted, "'"+strings.ReplaceAll(dir, "'", `'\''`)+"'")
	}
	script := fmt.Sprintf("%s\nGO_E2E_DIRS=(%s)\nif %s; then echo yes; else echo no; fi",
		funcText, strings.Join(quoted, " "), preloadFunc)

	out, err := exec.Command("bash", "-c", script).CombinedOutput() //nolint:gosec // G204: script text comes from the in-repo runner
	require.NoError(t, err, "running %s: %s", preloadFunc, string(out))

	switch answer := strings.TrimSpace(string(out)); answer {
	case "yes":
		return true
	case "no":
		return false
	default:
		t.Fatalf("unexpected answer from %s: %q", preloadFunc, answer)
		return false
	}
}

func TestRunE2EPreloadsPackageModeImageForMigrationSuite(t *testing.T) {
	funcText := extractShellFunc(t, runE2EScriptPath(t), preloadFunc)

	tests := []struct {
		name string
		dirs []string
		want bool
	}{
		{
			name: "When the migration suite is named explicitly it should preload",
			dirs: []string{"test/e2e/package_mode_migration"},
			want: true,
		},
		{
			name: "When the migration suite is named with a leading dot it should preload",
			dirs: []string{"./test/e2e/package_mode_migration"},
			want: true,
		},
		{
			name: "When the migration suite is named with a trailing slash it should preload",
			dirs: []string{"./test/e2e/package_mode_migration/"},
			want: true,
		},
		{
			name: "When the package-mode suite is named it should still preload",
			dirs: []string{"./test/e2e/package_mode"},
			want: true,
		},
		{
			name: "When the default wildcard is used it should preload",
			dirs: []string{"./test/e2e/..."},
			want: true,
		},
		{
			name: "When only an unrelated suite is named it should not preload",
			dirs: []string{"./test/e2e/rollout"},
			want: false,
		},
		{
			name: "When the migration suite rides along with an unrelated one it should preload",
			dirs: []string{"./test/e2e/rollout", "./test/e2e/package_mode_migration"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, preloads(t, funcText, tt.dirs...))
		})
	}
}

// A wildcard run selects every directory under test/e2e, including this one.
// The suite must therefore refuse to do anything until a deployment is handed
// to it on purpose; an unset or accidental value is not that.
//
// The environment is set explicitly in each case rather than read as it
// happens to be, so a developer who legitimately exported the opt-in before
// running unit checks does not get a failure here.
func TestMigrationSuiteStaysInertWithoutExplicitOptIn(t *testing.T) {
	t.Run("When the opt-in is absent the suite stays inert", func(t *testing.T) {
		t.Setenv(packagemodemigration.ExclusivityEnvVar, "")
		require.False(t, packagemodemigration.OptedIn(os.Getenv(packagemodemigration.ExclusivityEnvVar)))
	})

	t.Run("When the opt-in is set the suite activates", func(t *testing.T) {
		t.Setenv(packagemodemigration.ExclusivityEnvVar, "true")
		require.True(t, packagemodemigration.OptedIn(os.Getenv(packagemodemigration.ExclusivityEnvVar)))
	})

	t.Run("When the opt-in is a near miss the suite stays inert", func(t *testing.T) {
		t.Setenv(packagemodemigration.ExclusivityEnvVar, "ture")
		require.False(t, packagemodemigration.OptedIn(os.Getenv(packagemodemigration.ExclusivityEnvVar)))
	})
}

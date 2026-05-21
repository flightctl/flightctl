// mirror-images enumerates all RHEM container images for a given chart variant
// and generates skopeo copy commands suitable for an air-gapped installation.
//
// # Usage
//
//	./mirror-images --variant <variant> --dest-registry <host:port> [--execute]
//
// # Stories
//
//	EDM-3957  CLI scaffold and argument parsing
//	EDM-3958  Parse helm-chart-opts.yaml and generate skopeo commands
//	EDM-3959  Parse observability images from packaging/images/*/images.yaml
//	EDM-3960  Generate artifact manifest YAML
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/spf13/cobra"
)

// validVariants is the exhaustive list of supported chart variants.
// Any value not in this list is rejected at validation time.
var validVariants = []string{
	"community-el9",
	"community-el10",
	"redhat-el9",
	"redhat-el10",
}

// schemeRE matches URL scheme prefixes that must not appear in --dest-registry.
var schemeRE = regexp.MustCompile(`(?i)^https?://`)

// destRegistry is the caller-supplied destination registry (host:port).
// It is set in RunE and read by ImageToDest in mirror.go.
// Using a package-level variable keeps ImageToDest signature simple and mirrors
// the bash script's use of a global $DEST_REGISTRY.
var destRegistry string

// envOr returns the value of the environment variable named key, or fallback
// if the variable is unset or empty.  This mirrors the bash ${VAR:-default}
// pattern and lets the test suite override file paths without touching defaults.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// repoRoot attempts to resolve the flightctl repository root relative to the
// running binary's location.
//
// The binary is expected to be built from scripts/air-gap/mirror-images/, so
// the repo root is three directories above the binary:
//
//	bin/mirror-images → bin/ → repo root      (when run via `go build -o bin/`)
//
// If os.Executable() fails we fall back to the current working directory so
// that `go run ./scripts/air-gap/mirror-images` still works from the repo root.
func repoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	// Resolve symlinks (e.g. go run places a temp binary elsewhere)
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "."
	}
	// Walk up: binary dir → repo root (bin/ is one level below root)
	return filepath.Clean(filepath.Join(filepath.Dir(exe), ".."))
}

func main() {
	if err := NewRootCommand().Execute(); err != nil {
		// cobra already printed the error; just set the exit code.
		os.Exit(1)
	}
}

// NewRootCommand builds and returns the cobra root command for mirror-images.
func NewRootCommand() *cobra.Command {
	var (
		variant string
		execute bool
	)

	cmd := &cobra.Command{
		Use:   "mirror-images --variant <variant> --dest-registry <host:port> [--execute]",
		Short: "Enumerate RHEM artifacts and generate skopeo mirror commands for air-gapped installation.",
		Long: `mirror-images reads the flightctl Helm chart options and observability image files,
generates skopeo copy commands for all referenced container images, and writes a
machine-readable artifact manifest to the current directory.

By default the commands are printed to stdout (dry-run). Pass --execute to run them immediately.

Output:
  stdout                          — one skopeo copy command per image
  stderr                          — progress logs ([INFO] / [WARN] / [ERROR])
  artifact-manifest-<variant>.yaml — machine-readable artifact manifest

Examples:
  # Dry-run: print all commands for the community-el9 variant
  mirror-images --variant community-el9 --dest-registry local-registry.example.com:5000

  # Execute: mirror images to a running local registry
  mirror-images --variant redhat-el9 --dest-registry local-registry.example.com:5000 --execute`,

		// SilenceUsage prevents cobra from printing the full usage block on every
		// RunE error — our logError calls provide targeted messages instead.
		SilenceUsage: true,

		RunE: func(cmd *cobra.Command, args []string) error {
			// ----------------------------------------------------------------
			// Validate flags
			// ----------------------------------------------------------------

			if variant == "" {
				return fmt.Errorf("--variant is required (one of: %s)", strings.Join(validVariants, ", "))
			}

			if !isValidVariant(variant) {
				return fmt.Errorf("invalid variant %q — allowed values: %s", variant, strings.Join(validVariants, ", "))
			}

			if destRegistry == "" {
				return fmt.Errorf("--dest-registry is required (example: local-registry.example.com:5000)")
			}

			if schemeRE.MatchString(destRegistry) {
				return fmt.Errorf("--dest-registry must not include a URL scheme (https:// or http://)\nexample: local-registry.example.com:5000")
			}

			// ----------------------------------------------------------------
			// Resolve input file paths (env var overrides for testing)
			// ----------------------------------------------------------------

			root := repoRoot()
			helmChartOptsPath := envOr("HELM_CHART_OPTS", filepath.Join(root, "deploy/helm/helm-chart-opts.yaml"))
			chartYAMLPath := envOr("CHART_YAML", filepath.Join(root, "deploy/helm/flightctl/Chart.yaml"))
			obsEl9Path := envOr("OBS_IMAGES_EL9", filepath.Join(root, "packaging/images/el9/images.yaml"))
			obsEl10Path := envOr("OBS_IMAGES_EL10", filepath.Join(root, "packaging/images/el10/images.yaml"))
			obsRhel9Path := envOr("OBS_IMAGES_RHEL9", filepath.Join(root, "packaging/images/rhel9/images.yaml"))
			obsRhel10Path := envOr("OBS_IMAGES_RHEL10", filepath.Join(root, "packaging/images/rhel10/images.yaml"))
			rpmSpecPath := envOr("RPM_SPEC", filepath.Join(root, "packaging/rpm/flightctl.spec"))

			// Verify required files exist before doing any work
			for _, path := range []string{helmChartOptsPath, chartYAMLPath, rpmSpecPath} {
				if _, err := os.Stat(path); err != nil {
					return fmt.Errorf("required file not found: %s\nRun from the flightctl repository root or ensure the repo is up to date", path)
				}
			}

			// Check skopeo is available when --execute is requested
			if execute {
				if _, err := os.Stat("/usr/bin/skopeo"); err != nil {
					// Try PATH lookup via executer
					ex := executer.NewCommonExecuter()
					_, _, code := ex.Execute("skopeo", "--version")
					if code != 0 {
						return fmt.Errorf("skopeo is required when --execute is set but was not found\nInstall it with: dnf install skopeo")
					}
				}
			} else {
				// Warn early so the user knows --execute is needed to actually mirror
				ex := executer.NewCommonExecuter()
				_, _, code := ex.Execute("skopeo", "--version")
				if code != 0 {
					logWarn("skopeo not found — commands will be printed but not executed.")
				}
			}

			// ----------------------------------------------------------------
			// Execute the mirror workflow
			// ----------------------------------------------------------------

			logInfo("Starting artifact enumeration")
			logInfo("  Variant:          %s", variant)
			logInfo("  Dest registry:    %s", destRegistry)
			logInfo("  Execute commands: %v", execute)

			// Step 1: Read the chart appVersion (used as tag fallback for tagless images)
			appVersion, err := ReadAppVersion(chartYAMLPath)
			if err != nil {
				return fmt.Errorf("read chart version: %w", err)
			}
			logInfo("  Chart appVersion: %s (used as tag fallback)", appVersion)

			// Step 2: Parse image references from both YAML sources
			helmPairs, err := ParseHelmChartOpts(helmChartOptsPath, variant, appVersion)
			if err != nil {
				return fmt.Errorf("parse helm-chart-opts: %w", err)
			}

			obsPairs, err := ParseObsImages(obsEl9Path, obsEl10Path, obsRhel9Path, obsRhel10Path, variant)
			if err != nil {
				return fmt.Errorf("parse observability images: %w", err)
			}

			// Step 3: Merge and deduplicate (same image:tag from both sources → one command)
			all := append(helmPairs, obsPairs...)
			unique := Dedup(all)

			// Step 4: Print (and optionally execute) skopeo copy commands
			ctx := context.Background()
			exec := executer.NewCommonExecuter()
			if err := GenerateCommands(ctx, unique, execute, exec); err != nil {
				return fmt.Errorf("generate commands: %w", err)
			}

			// Step 5: Parse RPM requires for the manifest
			rpms, err := ParseRPMRequires(rpmSpecPath)
			if err != nil {
				// RPM parsing failure is non-fatal — the manifest will have an
				// incomplete rpms[] list but the image commands are already done.
				logWarn("Could not parse RPM requires from spec: %v", err)
				rpms = nil
			}

			// Step 6: Write the artifact manifest
			if err := WriteManifest(variant, unique, rpms); err != nil {
				return fmt.Errorf("write manifest: %w", err)
			}

			logInfo("Done.")
			if !execute {
				logInfo("Commands were printed but not executed.")
				logInfo("To execute, re-run with --execute, or pipe stdout to bash:")
				logInfo("  %s --variant %s --dest-registry %s | bash", os.Args[0], variant, destRegistry)
			}

			return nil
		},
	}

	// Register flags — these are the same flags as mirror-images.sh for a
	// drop-in replacement experience.
	cmd.Flags().StringVar(&variant, "variant", "", "Chart variant (community-el9 | community-el10 | redhat-el9 | redhat-el10)")
	cmd.Flags().StringVar(&destRegistry, "dest-registry", "", "Destination registry URL — no scheme, no trailing slash (e.g. local-registry.example.com:5000)")
	cmd.Flags().BoolVar(&execute, "execute", false, "Execute skopeo commands immediately in addition to printing them")

	return cmd
}

// isValidVariant reports whether v is in the validVariants list.
func isValidVariant(v string) bool {
	for _, allowed := range validVariants {
		if v == allowed {
			return true
		}
	}
	return false
}

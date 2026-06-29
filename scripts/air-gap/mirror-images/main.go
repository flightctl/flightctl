// flightctl-mirror-images enumerates all RHEM container images for a given chart variant
// and generates skopeo copy commands suitable for an air-gapped installation.
//
// # Usage
//
//	./flightctl-mirror-images --variant <variant> --dest-registry <host:port> [--execute]
//
// # How it works
//
// All image and RPM data is resolved at build time by "make generate-mirror-embed"
// (see scripts/air-gap/generate-embed/) and compiled into the binary.  The runtime
// tool simply reads the pre-resolved manifest, applies --tag-override if requested,
// and generates skopeo commands — no source checkout or data files required.
//
// To use a custom manifest (e.g. to pre-stage images for a future release):
//
//	MIRROR_MANIFEST=/path/to/custom-manifest.json flightctl-mirror-images --variant rhem-el9 ...
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
	"rhem-el9",
	"rhem-el10",
}

// schemeRE matches URL scheme prefixes that must not appear in --dest-registry.
var schemeRE = regexp.MustCompile(`(?i)^https?://`)

// destRegistry is the caller-supplied destination registry (host:port).
// It is set in RunE and read by ImageToDest in mirror.go.
// Using a package-level variable keeps ImageToDest signature simple and mirrors
// the bash script's use of a global $DEST_REGISTRY.
var destRegistry string

func main() {
	if err := NewRootCommand().Execute(); err != nil {
		// cobra already printed the error; just set the exit code.
		os.Exit(1)
	}
}

// validateFlags checks mutually exclusive and co-required flag combinations.
func validateFlags(variant, bundle string, execute, bundleRPMs, rpmReposync, rpmCreaterepo, agentOnly bool) error {
	if rpmReposync && rpmCreaterepo {
		return fmt.Errorf("--rpm-reposync and --rpm-createrepo are mutually exclusive")
	}
	if (rpmReposync || rpmCreaterepo) && !bundleRPMs && !agentOnly {
		return fmt.Errorf("--rpm-reposync and --rpm-createrepo require --bundle-rpms or --agent-only")
	}
	if agentOnly {
		if bundle == "" {
			return fmt.Errorf("--agent-only requires --bundle")
		}
		if execute {
			return fmt.Errorf("--agent-only and --execute are mutually exclusive")
		}
	}
	if !agentOnly && variant == "" {
		return fmt.Errorf("--variant is required (one of: %s)", strings.Join(validVariants, ", "))
	}
	if !agentOnly && !isValidVariant(variant) {
		return fmt.Errorf("invalid variant %q — allowed values: %s", variant, strings.Join(validVariants, ", "))
	}
	if execute && bundle != "" {
		return fmt.Errorf("--execute and --bundle are mutually exclusive")
	}
	if bundleRPMs && bundle == "" {
		return fmt.Errorf("--bundle-rpms requires --bundle")
	}
	return nil
}

// imageTagToRPMVersion converts an image tag (e.g. "1.2.0-rc3") to the
// equivalent RPM version string (e.g. "1.2.0~rc3"). RPM uses tildes (~) for
// pre-release suffixes so they sort before the release version; container image
// tags use hyphens instead.  A plain release version like "1.2.0" is unchanged.
func imageTagToRPMVersion(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == '-' {
			return tag[:i] + "~" + tag[i+1:]
		}
	}
	return tag
}

// pinRPMPackages appends the given version to each flightctl package name so
// that dnf downloads exactly that version rather than the latest available.
// Third-party packages (those that do not start with "flightctl-") are left
// unpinned so their upstream version is used unchanged.
func pinRPMPackages(packages []string, version string) []string {
	pinned := make([]string, len(packages))
	for i, p := range packages {
		if strings.HasPrefix(p, "flightctl-") || p == "flightctl" {
			pinned[i] = p + "-" + version
		} else {
			pinned[i] = p
		}
	}
	return pinned
}

// resolveRPMVersion pins flightctl RPM packages to the version derived from
// tagOverride when bundling RPMs, so the downloaded RPM version matches the
// bundled image tags. Only applies when tagOverride is set and the caller did
// not explicitly set --rpm-packages.
func resolveRPMVersion(packages []string, tagOverride string, shouldPin, packagesExplicitlySet bool) []string {
	if !shouldPin || tagOverride == "" || packagesExplicitlySet {
		return packages
	}
	effective := imageTagToRPMVersion(tagOverride)
	pinned := pinRPMPackages(packages, effective)
	logInfo("  RPM version pin:  %s", effective)
	return pinned
}

// excludePackages returns packages with any entry found in exclude removed.
func excludePackages(packages, exclude []string) []string {
	if len(exclude) == 0 {
		return packages
	}
	excluded := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excluded[e] = true
	}
	result := make([]string, 0, len(packages))
	for _, p := range packages {
		if !excluded[p] {
			result = append(result, p)
		}
	}
	return result
}

// runAgentOnlyBundle creates an RPM-only offline bundle without any image content.
func runAgentOnlyBundle(ctx context.Context, rpmPackages, installPackages []string, rpmRepoURL, bundle string, rpmReposync, rpmCreaterepo bool, exec executer.Executer) error {
	tmpDir, err := os.MkdirTemp("", "flightctl-bundle-*")
	if err != nil {
		return fmt.Errorf("create temp bundle dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	logInfo("Starting agent-only bundle")
	logInfo("  Download:   %s", strings.Join(rpmPackages, ", "))
	logInfo("  Install:    %s", strings.Join(installPackages, ", "))
	logInfo("  Reposync:   %v", rpmReposync)
	logInfo("  Createrepo: %v", rpmCreaterepo)
	logInfo("  Output:     %s", bundle)

	if err := DownloadRPMs(ctx, rpmPackages, rpmRepoURL, tmpDir, rpmReposync, rpmCreaterepo, exec); err != nil {
		return fmt.Errorf("download RPMs: %w", err)
	}
	if err := WriteInstallRPMsScript(tmpDir, installPackages, rpmReposync, rpmCreaterepo); err != nil {
		return fmt.Errorf("write install-rpms script: %w", err)
	}
	if err := CreateArchive(tmpDir, bundle); err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	logInfo("Agent bundle created: %s", bundle)
	return nil
}

// ensureSkopeoInRPMs returns rpmPackages with skopeo appended if not already present.
func ensureSkopeoInRPMs(rpmPackages []string) []string {
	for _, p := range rpmPackages {
		if p == "skopeo" {
			return rpmPackages
		}
	}
	return append(rpmPackages, "skopeo")
}

// runBundleMode creates a self-contained offline archive with images and optional RPMs.
func runBundleMode(ctx context.Context, unique []ImagePair, bundle, variant string, bundleRPMs bool, rpmPackages, installPackages []string, rpmRepoURL string, rpmReposync, rpmCreaterepo bool, manifestRPMs []string, exec executer.Executer) error {
	tmpDir, err := os.MkdirTemp("", "flightctl-bundle-*")
	if err != nil {
		return fmt.Errorf("create temp bundle dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// registry:2 is required on the air-gapped target machine to serve
	// the mirrored images locally. Include it in every bundle automatically.
	bundleImages := append(unique, ImagePair{
		Source: "docker.io/library/registry:2",
		Dest:   destRegistry + "/library/registry:2",
	})

	if err := BundleImages(ctx, bundleImages, tmpDir, exec); err != nil {
		logWarn("Some images failed to bundle: %v", err)
	}
	if err := WriteImportScript(tmpDir, bundleImages, variant); err != nil {
		return fmt.Errorf("write import script: %w", err)
	}
	if bundleRPMs {
		effectiveRPMs := ensureSkopeoInRPMs(rpmPackages)
		if err := DownloadRPMs(ctx, effectiveRPMs, rpmRepoURL, tmpDir, rpmReposync, rpmCreaterepo, exec); err != nil {
			return fmt.Errorf("download RPMs: %w", err)
		}
		if err := WriteInstallRPMsScript(tmpDir, installPackages, rpmReposync, rpmCreaterepo); err != nil {
			return fmt.Errorf("write install-rpms script: %w", err)
		}
	}

	origDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		return fmt.Errorf("change to bundle temp dir: %w", err)
	}
	if mErr := WriteManifest(variant, bundleImages, manifestRPMs); mErr != nil {
		logWarn("write manifest: %v", mErr)
	}
	if err := os.Chdir(origDir); err != nil {
		return fmt.Errorf("restore working directory: %w", err)
	}

	if err := CreateArchive(tmpDir, bundle); err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	logInfo("Bundle created: %s", bundle)
	return nil
}

// NewRootCommand builds and returns the cobra root command for flightctl-mirror-images.
func NewRootCommand() *cobra.Command {
	var (
		variant       string
		execute       bool
		insecure      bool
		tagOverride   string
		bundle        string
		bundleRPMs    bool
		rpmPackages   []string
		rpmExclude    []string
		rpmRepoURL    string
		rpmReposync   bool
		rpmCreaterepo bool
		agentOnly     bool
	)

	cmd := &cobra.Command{
		Use:   "flightctl-mirror-images --variant <variant> [--dest-registry <host:port>] [--bundle <path>] [--execute] [--insecure]",
		Short: "Enumerate RHEM artifacts and generate skopeo mirror commands for air-gapped installation.",
		Long: `flightctl-mirror-images reads a pre-resolved image manifest embedded at build time
and generates skopeo copy commands for all referenced container images.

The binary is self-contained — no source checkout or data files are required.
Image and RPM lists for all variants are compiled in when the binary is built,
version-matched to the release by construction.

To use a custom manifest (e.g. to pre-stage images for a future RHEM upgrade
before the corresponding RPM is installed):

  MIRROR_MANIFEST=/path/to/custom-manifest.json \
    flightctl-mirror-images --variant rhem-el9 --dest-registry registry.example.com:5000

Live-push mode (--execute):
  Copies images directly to a running registry. Requires --dest-registry.

Bundle mode (--bundle <path>):
  Creates a self-contained offline archive (.tar.gz) at the specified path.
  The archive includes all images (via skopeo dir: transport), an import.sh
  script to push images to any registry on the air-gapped machine, and
  optionally RPMs and an install-rpms.sh script (see --bundle-rpms).
  No intermediate registry is required. Namespaces are fully preserved.

Output:
  stdout                           — one skopeo copy command per image (non-bundle mode)
  stderr                           — progress logs ([INFO] / [WARN] / [ERROR])
  artifact-manifest-<variant>.yaml — machine-readable artifact manifest

Examples:
  # Dry-run: print all commands for the community-el9 variant
  flightctl-mirror-images --variant community-el9 --dest-registry local-registry.example.com:5000

  # Execute: mirror images to a running local registry
  flightctl-mirror-images --variant rhem-el9 --dest-registry local-registry.example.com:5000 --execute

  # Bundle: create offline archive with all images
  flightctl-mirror-images --variant community-el9 --bundle ~/flightctl-bundle.tar.gz

  # Bundle: include RPMs for bare-metal quadlet installation
  flightctl-mirror-images --variant community-el9 --bundle ~/flightctl-bundle.tar.gz --bundle-rpms

  # Bundle with custom dest registry written into import.sh
  flightctl-mirror-images --variant community-el9 --dest-registry myregistry.local:5000 --bundle ~/flightctl-bundle.tar.gz

  # Execute: mirror to an HTTP (non-TLS) local registry
  flightctl-mirror-images --variant community-el9 --dest-registry localhost:5000 --execute --insecure

  # Agent/CLI bundle for edge devices (no --variant required)
  flightctl-mirror-images --agent-only --bundle ~/flightctl-agent-bundle.tar.gz

  # Server bundle with full repo mirror and metadata (requires dnf-plugins-core)
  flightctl-mirror-images --variant community-el9 --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms --rpm-reposync

  # Server bundle with generated repo metadata after targeted download
  flightctl-mirror-images --variant community-el9 --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms --rpm-createrepo

  # Bundle with explicit version pin — image tags and RPM version stay in sync automatically
  flightctl-mirror-images --variant community-el9 --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms --rpm-createrepo --tag-override 1.2.0-rc3`,

		// SilenceUsage prevents cobra from printing the full usage block on every
		// RunE error — our logError calls provide targeted messages instead.
		SilenceUsage: true,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateFlags(variant, bundle, execute, bundleRPMs, rpmReposync, rpmCreaterepo, agentOnly); err != nil {
				return err
			}

			if agentOnly && !cmd.Flags().Changed("rpm-packages") {
				rpmPackages = []string{"flightctl-agent", "flightctl-cli", "open-vm-tools", "ignition", "afterburn", "cloud-init"}
			}

			rpmPackages = resolveRPMVersion(rpmPackages, tagOverride,
				bundleRPMs || agentOnly, cmd.Flags().Changed("rpm-packages"))

			installPackages := excludePackages(rpmPackages, rpmExclude)
			if len(rpmExclude) > 0 {
				logInfo("  Excluded from install: %s (RPMs still downloaded for manual use)", strings.Join(rpmExclude, ", "))
			}

			if agentOnly {
				ctx := context.Background()
				exec := executer.NewCommonExecuter()
				return runAgentOnlyBundle(ctx, rpmPackages, installPackages, rpmRepoURL, bundle, rpmReposync, rpmCreaterepo, exec)
			}

			// Default dest-registry to localhost:5000 for bundle mode (used only in import.sh)
			if bundle != "" && destRegistry == "" {
				destRegistry = "localhost:5000"
			}
			if destRegistry == "" {
				return fmt.Errorf("--dest-registry is required (example: local-registry.example.com:5000)")
			}
			if schemeRE.MatchString(destRegistry) {
				return fmt.Errorf("--dest-registry must not include a URL scheme (https:// or http://)\nexample: local-registry.example.com:5000")
			}

			m, err := loadBuildManifest()
			if err != nil {
				return fmt.Errorf("load mirror manifest: %w", err)
			}

			// Check skopeo availability — required for both --execute and --bundle
			if execute || bundle != "" {
				if _, err := os.Stat("/usr/bin/skopeo"); err != nil {
					ex := executer.NewCommonExecuter()
					_, _, code := ex.Execute("skopeo", "--version")
					if code != 0 {
						return fmt.Errorf("skopeo is required but was not found\nInstall it with: dnf install skopeo")
					}
				}
			} else {
				ex := executer.NewCommonExecuter()
				_, _, code := ex.Execute("skopeo", "--version")
				if code != 0 {
					logWarn("skopeo not found — commands will be printed but not executed.")
				}
			}

			logInfo("Starting artifact enumeration")
			logInfo("  Variant:          %s", variant)
			if bundle != "" {
				logInfo("  Bundle output:    %s", bundle)
				logInfo("  Import registry:  %s (written into import.sh)", destRegistry)
				logInfo("  Bundle RPMs:      %v", bundleRPMs)
			} else {
				logInfo("  Dest registry:    %s", destRegistry)
				logInfo("  Execute commands: %v", execute)
				logInfo("  Insecure (HTTP):  %v", insecure)
			}

			appVersion := m.AppVersion
			logInfo("  Chart appVersion: %s", appVersion)

			effectiveTag := appVersion
			if tagOverride != "" {
				effectiveTag = tagOverride
				logInfo("  Tag override:     %s (overrides appVersion for untagged images)", effectiveTag)
			} else if appVersion == "latest" {
				logWarn("appVersion is 'latest' — images without explicit tags will be mirrored as :latest.")
				logWarn("This will NOT match quadlet files from versioned RPMs (e.g. v1.1.2).")
				logWarn("Find the target RPM version: rpm -q --qf '%%{VERSION}' flightctl-agent")
				logWarn("Then re-run with --tag-override:")
				if bundle != "" {
					logWarn("  flightctl-mirror-images --variant %s --bundle %s --tag-override <version>", variant, bundle)
				} else {
					logWarn("  flightctl-mirror-images --variant %s --dest-registry %s --tag-override <version>", variant, destRegistry)
				}
			} else {
				logInfo("  Effective tag:    %s (for untagged images)", effectiveTag)
			}

			unique, manifestRPMs, err := resolveVariant(m, variant, effectiveTag)
			if err != nil {
				return err
			}

			ctx := context.Background()
			exec := executer.NewCommonExecuter()

			if bundle != "" {
				return runBundleMode(ctx, unique, bundle, variant, bundleRPMs, rpmPackages, installPackages, rpmRepoURL, rpmReposync, rpmCreaterepo, manifestRPMs, exec)
			}

			if err := GenerateCommands(ctx, unique, execute, insecure, exec); err != nil {
				return fmt.Errorf("generate commands: %w", err)
			}

			if err := WriteManifest(variant, unique, manifestRPMs); err != nil {
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

	// Register flags.
	cmd.Flags().StringVar(&variant, "variant", "", "Chart variant (community-el9 | community-el10 | rhem-el9 | rhem-el10)")
	cmd.Flags().StringVar(&destRegistry, "dest-registry", "", "Destination registry URL — no scheme, no trailing slash (e.g. local-registry.example.com:5000)")
	cmd.Flags().BoolVar(&execute, "execute", false, "Execute skopeo commands immediately in addition to printing them")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Disable TLS verification for the destination registry (required for HTTP registries)")
	cmd.Flags().StringVar(&tagOverride, "tag-override", "", "Tag to use for flightctl service images (e.g. v1.1.2, latest). Overrides appVersion for images without a pinned tag. Use to select a release version when running from a dev branch.")
	cmd.Flags().StringVar(&bundle, "bundle", "", "Create an offline bundle archive (.tar.gz) at the specified path (mutually exclusive with --execute)")
	cmd.Flags().BoolVar(&bundleRPMs, "bundle-rpms", false, "Include RPMs in the bundle for bare-metal quadlet installation (requires --bundle)")
	cmd.Flags().StringSliceVar(&rpmPackages, "rpm-packages", []string{"flightctl-services", "flightctl-cli", "flightctl-agent"}, "RPM package names to download into the bundle (comma-separated). The default includes flightctl-cli for server-side fleet management and flightctl-agent for offline image building — the agent RPM is not enabled on the server but is available in the bundle rpms/ directory for embedding into device OS images.")
	cmd.Flags().StringSliceVar(&rpmExclude, "rpm-exclude", nil, "RPM package names to download but exclude from auto-installation (comma-separated). Excluded packages are still present in rpms/ for manual use (e.g. embedding into device OS images).")
	cmd.Flags().StringVar(&rpmRepoURL, "rpm-repo-url", "https://rpm.flightctl.io/flightctl-epel.repo", "URL of the .repo file to configure dnf for RPM downloads")
	cmd.Flags().BoolVar(&rpmReposync, "rpm-reposync", false, "Mirror the full FlightCtl RPM repository using 'dnf reposync' (includes repodata/; requires dnf-plugins-core; mutually exclusive with --rpm-createrepo)")
	cmd.Flags().BoolVar(&rpmCreaterepo, "rpm-createrepo", false, "Generate repodata/ after 'dnf download' using 'createrepo_c' so the bundle can be used as a local dnf repository source (mutually exclusive with --rpm-reposync)")
	cmd.Flags().BoolVar(&agentOnly, "agent-only", false, "Create an RPM-only bundle for edge device agent installation — skips image bundling, does not require --variant, defaults --rpm-packages to flightctl-agent,flightctl-cli,open-vm-tools,ignition,afterburn,cloud-init")

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

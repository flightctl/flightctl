// refresh-base-images detects new UBI base image versions, updates
// images.yaml base image tag references, and opens pull requests. It is
// used by the refresh-base-images GHA workflow to automate base image
// updates.
//
// Subcommands:
//
//	detect     - Query registries for latest tags, output JSON summary
//	update     - Update images.yaml base tags from an updates JSON
//	create-pr  - Update images.yaml, commit, push, and open/update a GitHub PR
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// ImageUpdate tracks the update status for a single image type.
type ImageUpdate struct {
	Updated bool   `json:"updated"`
	Tag     string `json:"tag"`
}

// VersionUpdate tracks all image updates for a single EL version.
type VersionUpdate struct {
	Any       bool        `json:"any"`
	Micro     ImageUpdate `json:"micro"`
	Minimal   ImageUpdate `json:"minimal"`
	GoToolset ImageUpdate `json:"gotoolset"`
}

// BuildMatrixEntry describes a single arch+version build job for GHA.
type BuildMatrixEntry struct {
	RelVer   string `json:"relver"`
	MicroTag string `json:"micro_tag"`
	Arch     string `json:"arch"`
	Runner   string `json:"runner"`
}

// ManifestMatrixEntry describes a single manifest-list creation job.
type ManifestMatrixEntry struct {
	RelVer   string `json:"relver"`
	MicroTag string `json:"micro_tag"`
}

// DetectResult is the full output of the detect subcommand.
type DetectResult struct {
	BuildMatrix    []BuildMatrixEntry       `json:"build_matrix"`
	ManifestMatrix []ManifestMatrixEntry    `json:"manifest_matrix"`
	Updates        map[string]VersionUpdate `json:"updates"`
	AnyUpdated     bool                     `json:"any_updated"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: refresh-base-images <detect|update|create-pr> [flags]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "detect":
		runDetect(os.Args[2:])
	case "update":
		runUpdate(os.Args[2:])
	case "create-pr":
		runCreatePR(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q. Use 'detect', 'update', or 'create-pr'.\n", os.Args[1])
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// detect
// ---------------------------------------------------------------------------

func runDetect(args []string) {
	fs := flag.NewFlagSet("detect", flag.ExitOnError)
	imagesDir := fs.String("images-dir", "packaging/images", "Path to the images directory")
	goMod := fs.String("go-mod", "go.mod", "Path to go.mod file")
	ubiRegistry := fs.String("ubi-registry", "registry.access.redhat.com", "UBI image registry")
	imageRegistry := fs.String("image-registry", "quay.io/flightctl", "flightctl image registry")
	githubOutput := fs.String("github-output", "", "Path to GITHUB_OUTPUT file (optional)")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	client := NewRegistryClient()

	result, err := detect(ctx, client, *imagesDir, *goMod, *ubiRegistry, *imageRegistry)
	if err != nil {
		log.Fatalf("Detection failed: %v", err)
	}

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("Marshaling result: %v", err)
	}
	fmt.Println(string(output))

	if *githubOutput != "" {
		if err := writeGitHubOutputs(*githubOutput, result); err != nil {
			log.Fatalf("Writing GitHub outputs: %v", err)
		}
	}
}

func detect(ctx context.Context, lister TagLister, imagesDir, goModPath, ubiRegistry, imageRegistry string) (*DetectResult, error) {
	goModContent, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", goModPath, err)
	}
	goMinor, err := ExtractGoMinorVersion(string(goModContent))
	if err != nil {
		return nil, err
	}
	goPattern := GoToolsetTagPattern(goMinor)
	log.Printf("Go minor version: %s", goMinor)

	result := &DetectResult{
		BuildMatrix:    []BuildMatrixEntry{},
		ManifestMatrix: []ManifestMatrixEntry{},
		Updates:        make(map[string]VersionUpdate),
	}

	for _, ver := range []string{"9", "10"} {
		yamlPath := ImagesYAMLPath(imagesDir, ver)
		ubiPattern := UBITagPattern(ver)
		vu := VersionUpdate{}

		entries, _, err := ReadImagesYAML(yamlPath)
		if err != nil {
			return nil, fmt.Errorf("reading images.yaml for el%s: %w", ver, err)
		}

		// Check ubi-micro (flightctl-base) — read current tag from images.yaml.
		// If no entry references flightctl-base (ErrBaseImageNotFound), skip
		// micro detection for this EL version rather than treating "" as "changed".
		baseImage := imageRegistry + "/flightctl-base"
		currentMicro, err := GetCurrentTagForImage(entries, baseImage)
		if err != nil && !errors.Is(err, ErrBaseImageNotFound) {
			return nil, fmt.Errorf("el%s flightctl-base: %w", ver, err)
		}
		if currentMicro != "" {
			microRepo := fmt.Sprintf("ubi%s/ubi-micro", ver)
			microTags, err := lister.ListTags(ctx, ubiRegistry, microRepo)
			if err != nil {
				return nil, fmt.Errorf("listing tags for %s/%s: %w", ubiRegistry, microRepo, err)
			}
			latestMicro := LatestMatchingTag(microTags, ubiPattern)
			if latestMicro != "" && latestMicro != currentMicro {
				vu.Micro = ImageUpdate{Updated: true, Tag: latestMicro}
				vu.Any = true
				result.AnyUpdated = true
				result.BuildMatrix = append(result.BuildMatrix,
					BuildMatrixEntry{RelVer: ver, MicroTag: latestMicro, Arch: "amd64", Runner: "ubuntu-24.04"},
					BuildMatrixEntry{RelVer: ver, MicroTag: latestMicro, Arch: "arm64", Runner: "ubuntu-24.04-arm"},
				)
				result.ManifestMatrix = append(result.ManifestMatrix,
					ManifestMatrixEntry{RelVer: ver, MicroTag: latestMicro},
				)
			} else {
				vu.Micro = ImageUpdate{Updated: false, Tag: currentMicro}
			}
			log.Printf("el%s ubi-micro: current=%s latest=%s updated=%v", ver, currentMicro, vu.Micro.Tag, vu.Micro.Updated)
		} else {
			vu.Micro = ImageUpdate{Updated: false, Tag: ""}
			log.Printf("el%s ubi-micro: no flightctl-base entry in images.yaml, skipping", ver)
		}

		// Check ubi-minimal — read current tag from images.yaml.
		minimalImage := fmt.Sprintf("%s/ubi%s/ubi-minimal", ubiRegistry, ver)
		currentMinimal, err := GetCurrentTagForImage(entries, minimalImage)
		if err != nil && !errors.Is(err, ErrBaseImageNotFound) {
			return nil, fmt.Errorf("el%s ubi-minimal: %w", ver, err)
		}
		if currentMinimal != "" {
			minimalRepo := fmt.Sprintf("ubi%s/ubi-minimal", ver)
			minimalTags, err := lister.ListTags(ctx, ubiRegistry, minimalRepo)
			if err != nil {
				return nil, fmt.Errorf("listing tags for %s/%s: %w", ubiRegistry, minimalRepo, err)
			}
			latestMinimal := LatestMatchingTag(minimalTags, ubiPattern)
			if latestMinimal != "" && latestMinimal != currentMinimal {
				vu.Minimal = ImageUpdate{Updated: true, Tag: latestMinimal}
				vu.Any = true
				result.AnyUpdated = true
			} else {
				vu.Minimal = ImageUpdate{Updated: false, Tag: currentMinimal}
			}
		} else {
			vu.Minimal = ImageUpdate{Updated: false, Tag: ""}
		}
		log.Printf("el%s ubi-minimal: current=%s latest=%s updated=%v", ver, currentMinimal, vu.Minimal.Tag, vu.Minimal.Updated)

		// Check go-toolset — read current tag from images.yaml.
		// Skip if no entry references go-toolset for this EL version.
		goToolsetImage := fmt.Sprintf("%s/ubi%s/go-toolset", ubiRegistry, ver)
		currentGoToolset, err := GetCurrentTagForImage(entries, goToolsetImage)
		if err != nil && !errors.Is(err, ErrBaseImageNotFound) {
			return nil, fmt.Errorf("el%s go-toolset: %w", ver, err)
		}
		if currentGoToolset != "" {
			goToolsetRepo := fmt.Sprintf("ubi%s/go-toolset", ver)
			goToolsetTags, err := lister.ListTags(ctx, ubiRegistry, goToolsetRepo)
			if err != nil {
				return nil, fmt.Errorf("listing tags for %s/%s: %w", ubiRegistry, goToolsetRepo, err)
			}
			latestGoToolset := LatestMatchingTag(goToolsetTags, goPattern)
			if latestGoToolset != "" && latestGoToolset != currentGoToolset {
				vu.GoToolset = ImageUpdate{Updated: true, Tag: latestGoToolset}
				vu.Any = true
				result.AnyUpdated = true
			} else {
				vu.GoToolset = ImageUpdate{Updated: false, Tag: currentGoToolset}
			}
			log.Printf("el%s go-toolset: current=%s latest=%s updated=%v", ver, currentGoToolset, vu.GoToolset.Tag, vu.GoToolset.Updated)
		} else {
			vu.GoToolset = ImageUpdate{Updated: false, Tag: ""}
			log.Printf("el%s go-toolset: no entry in images.yaml, skipping", ver)
		}

		result.Updates[ver] = vu
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// update
// ---------------------------------------------------------------------------

func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	imagesDir := fs.String("images-dir", "packaging/images", "Path to the images directory")
	ubiRegistry := fs.String("ubi-registry", "registry.access.redhat.com", "UBI image registry")
	imageRegistry := fs.String("image-registry", "quay.io/flightctl", "flightctl image registry")
	updatesJSON := fs.String("updates-json", "", "JSON string with update information (from detect)")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *updatesJSON == "" {
		log.Fatal("--updates-json is required")
	}

	var updates map[string]VersionUpdate
	if err := json.Unmarshal([]byte(*updatesJSON), &updates); err != nil {
		log.Fatalf("Parsing updates JSON: %v", err)
	}

	if err := applyUpdates(*imagesDir, updates, *ubiRegistry, *imageRegistry); err != nil {
		log.Fatalf("Applying updates: %v", err)
	}

	log.Println("images.yaml updates applied successfully")
}

// allowedVersions restricts which EL version keys are accepted to prevent
// path traversal via crafted updates JSON.
var allowedVersions = map[string]bool{"9": true, "10": true}

func applyUpdates(imagesDir string, updates map[string]VersionUpdate, ubiRegistry, imageRegistry string) error {
	versions := make([]string, 0, len(updates))
	for ver := range updates {
		if !allowedVersions[ver] {
			return fmt.Errorf("unsupported EL version %q (allowed: 9, 10)", ver)
		}
		versions = append(versions, ver)
	}
	sort.Strings(versions)

	for _, ver := range versions {
		vu := updates[ver]
		if !vu.Any {
			continue
		}

		yamlPath := ImagesYAMLPath(imagesDir, ver)
		entries, doc, err := ReadImagesYAML(yamlPath)
		if err != nil {
			return fmt.Errorf("reading images.yaml for el%s: %w", ver, err)
		}

		changed := false

		// Build a map of image prefix -> new tag for lookup.
		imageUpdates := make(map[string]string)
		if vu.Micro.Updated {
			imageUpdates[imageRegistry+"/flightctl-base"] = vu.Micro.Tag
		}
		if vu.Minimal.Updated {
			imageUpdates[fmt.Sprintf("%s/ubi%s/ubi-minimal", ubiRegistry, ver)] = vu.Minimal.Tag
		}
		if vu.GoToolset.Updated {
			imageUpdates[fmt.Sprintf("%s/ubi%s/go-toolset", ubiRegistry, ver)] = vu.GoToolset.Tag
		}

		// Update each container's base ref fields in images.yaml.
		for imagePrefix, newTag := range imageUpdates {
			containers := ContainersWithBase(entries, imagePrefix)
			newRef := imagePrefix + ":" + newTag
			for _, c := range containers {
				if err := UpdateNodeField(doc, c.Name, c.Field, newRef); err != nil {
					return fmt.Errorf("updating %s.%s: %w", c.Name, c.Field, err)
				}
				log.Printf("  el%s %s.%s -> %s", ver, c.Name, c.Field, newRef)
				changed = true
			}
		}

		if changed {
			if err := WriteImagesYAML(yamlPath, doc); err != nil {
				return fmt.Errorf("writing images.yaml for el%s: %w", ver, err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// create-pr
// ---------------------------------------------------------------------------

// gitRunner abstracts git command execution for testing.
type gitRunner interface {
	run(args ...string) error
	runOutput(args ...string) (string, error)
}

type realGitRunner struct{}

func (realGitRunner) run(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (realGitRunner) runOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func runCreatePR(args []string) {
	fs := flag.NewFlagSet("create-pr", flag.ExitOnError)
	imagesDir := fs.String("images-dir", "packaging/images", "Path to the images directory")
	ubiRegistry := fs.String("ubi-registry", "registry.access.redhat.com", "UBI image registry")
	imageRegistry := fs.String("image-registry", "quay.io/flightctl", "flightctl image registry")
	updatesJSON := fs.String("updates-json", "", "JSON string with update information (from detect)")
	repo := fs.String("repo", "", "GitHub repository (owner/repo)")
	branch := fs.String("branch", "auto/refresh-base-images", "Branch name for the PR")
	baseBranch := fs.String("base", "main", "Base branch for the PR")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		log.Fatal("GH_TOKEN or GITHUB_TOKEN environment variable is required")
	}

	if *updatesJSON == "" {
		log.Fatal("--updates-json is required")
	}
	if *repo == "" {
		// Fall back to GITHUB_REPOSITORY env var (set by GHA).
		*repo = os.Getenv("GITHUB_REPOSITORY")
		if *repo == "" {
			log.Fatal("--repo or GITHUB_REPOSITORY is required")
		}
	}

	var updates map[string]VersionUpdate
	if err := json.Unmarshal([]byte(*updatesJSON), &updates); err != nil {
		log.Fatalf("Parsing updates JSON: %v", err)
	}

	gh := NewGitHubClient(token)
	git := realGitRunner{}

	if err := createPR(context.Background(), gh, git, *imagesDir, updates, *ubiRegistry, *imageRegistry, *repo, *branch, *baseBranch); err != nil {
		log.Fatalf("create-pr failed: %v", err)
	}
}

func createPR(ctx context.Context, gh *GitHubClient, git gitRunner, imagesDir string, updates map[string]VersionUpdate, ubiRegistry, imageRegistry, repo, branch, baseBranch string) error {
	// 1. Apply images.yaml updates.
	if err := applyUpdates(imagesDir, updates, ubiRegistry, imageRegistry); err != nil {
		return fmt.Errorf("applying updates: %w", err)
	}

	// 2. Configure git and create branch.
	if err := git.run("config", "user.name", "github-actions[bot]"); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := git.run("config", "user.email", "github-actions[bot]@users.noreply.github.com"); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	if err := git.run("checkout", "-B", branch); err != nil {
		return fmt.Errorf("git checkout: %w", err)
	}

	// 3. Stage and check for changes.
	if err := git.run("add", "packaging/images/"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// git diff --quiet exits 1 when there ARE changes, 0 when clean.
	if err := git.run("diff", "--staged", "--quiet"); err == nil {
		log.Println("No changes to commit")
		return nil
	}

	// 4. Commit and push.
	if err := git.run("commit", "-m", "NO-ISSUE: Refresh base image references"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := git.run("push", "--force", "origin", branch); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	// 5. Create or update PR via GitHub API.
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format %q, expected owner/repo", repo)
	}
	owner, repoName := parts[0], parts[1]

	prBody := BuildPRBody(updates, repo)
	prTitle := "NO-ISSUE: Refresh base image references"

	existing, err := gh.ListOpenPRs(ctx, owner, repoName, branch)
	if err != nil {
		return fmt.Errorf("checking existing PRs: %w", err)
	}

	if len(existing) > 0 {
		if err := gh.UpdatePR(ctx, owner, repoName, existing[0].Number, prBody); err != nil {
			return fmt.Errorf("updating PR #%d: %w", existing[0].Number, err)
		}
		log.Printf("Updated existing PR #%d", existing[0].Number)
	} else {
		pr, err := gh.CreatePR(ctx, owner, repoName, prTitle, prBody, branch, baseBranch)
		if err != nil {
			return fmt.Errorf("creating PR: %w", err)
		}
		log.Printf("Created PR #%d: %s", pr.Number, pr.HTMLURL)
	}

	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeGitHubOutputs(path string, result *DetectResult) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buildMatrix, err := json.Marshal(result.BuildMatrix)
	if err != nil {
		return fmt.Errorf("marshaling build_matrix: %w", err)
	}
	manifestMatrix, err := json.Marshal(result.ManifestMatrix)
	if err != nil {
		return fmt.Errorf("marshaling manifest_matrix: %w", err)
	}
	updatesJSON, err := json.Marshal(result.Updates)
	if err != nil {
		return fmt.Errorf("marshaling updates_json: %w", err)
	}

	lines := []string{
		fmt.Sprintf("build_matrix=%s", buildMatrix),
		fmt.Sprintf("manifest_matrix=%s", manifestMatrix),
		fmt.Sprintf("updates_json=%s", updatesJSON),
		fmt.Sprintf("any_updated=%v", result.AnyUpdated),
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(f, line); err != nil {
			return err
		}
	}

	return nil
}

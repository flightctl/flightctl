package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
)

// BundleImages copies each image from the source registry directly into a
// local directory using the skopeo dir: transport.  The directory layout
// mirrors the source namespace path so the import script can push each image
// to the correct namespaced path on the air-gapped registry.
//
// Example layout:
//
//	bundleDir/images/flightctl/flightctl-api-el9:latest/
//	bundleDir/images/sclorg/postgresql-16-c9s:20250214/
//	bundleDir/images/library/redis:7.4.1/
//
// This approach preserves namespaces — unlike skopeo sync --src yaml --dest dir
// which strips namespace prefixes from directory names.
// BundleImages copies each image in pairs into bundleDir/images/ using skopeo.
// It returns the subset of pairs that were successfully bundled alongside any
// error describing which images failed.  Callers must use the returned slice
// (not the original pairs) when generating the import script, so that the
// script only references images that are actually present in the bundle.
func BundleImages(ctx context.Context, pairs []ImagePair, bundleDir string, exec executer.Executer) ([]ImagePair, error) {
	imagesDir := filepath.Join(bundleDir, "images")
	logInfo("Bundling %d images to %s...", len(pairs), imagesDir)

	var failed []string
	var bundled []ImagePair
	for i, p := range pairs {
		// Strip the source registry host to get the namespace/name:tag path.
		// "quay.io/flightctl/flightctl-api-el9:latest" → "flightctl/flightctl-api-el9:latest"
		parts := strings.SplitN(p.Source, "/", 2)
		if len(parts) < 2 {
			logError("Cannot parse source %q — skipping", p.Source)
			failed = append(failed, p.Source)
			continue
		}
		imageRelPath := parts[1] // e.g. "flightctl/flightctl-api-el9:latest"
		destPath := filepath.Join(imagesDir, imageRelPath)

		// Ensure namespace parent directory exists before skopeo writes into it.
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			logError("mkdir %s: %v — skipping", filepath.Dir(destPath), err)
			failed = append(failed, p.Source)
			continue
		}

		logInfo("[%d/%d] Bundling %s", i+1, len(pairs), p.Source)
		_, stderr, exitCode := exec.ExecuteWithContext(ctx, "skopeo",
			"copy", "docker://"+p.Source, "dir:"+destPath)
		if exitCode != 0 {
			logError("skopeo copy failed for %s: %s — continuing", p.Source, strings.TrimSpace(stderr))
			failed = append(failed, p.Source)
			continue
		}
		bundled = append(bundled, p)
	}

	if len(failed) > 0 {
		return bundled, fmt.Errorf("%d image(s) failed to bundle: %s", len(failed), strings.Join(failed, ", "))
	}
	return bundled, nil
}

// DownloadRPMs fetches RPMs into bundleDir/rpms/ using one of two strategies:
//
//   - reposync=true:  runs `dnf reposync` to mirror the entire FlightCtl repository
//     including repodata/ metadata.  The layout is bundleDir/rpms/flightctl/.
//     Requires dnf-plugins-core on the prep machine.  The packages slice is
//     ignored — reposync mirrors the whole repo.
//   - reposync=false: runs `dnf download --resolve` to download only the specified
//     packages and their transitive dependencies.  The layout is a flat
//     bundleDir/rpms/ directory containing *.rpm files.  If createrepo=true,
//     `createrepo_c` is run afterward to generate bundleDir/rpms/repodata/.
//
// If repoURL is non-empty, the .repo file at that URL is fetched and placed in
// a temporary directory that is appended to dnf's reposdir so system repos
// remain active for dependency resolution alongside the flightctl repo.
// sudoPrompt runs `sudo -v` with the terminal attached so the user is
// prompted for their password interactively.  This caches credentials for
// subsequent non-interactive `sudo -n` calls made by the RPM download steps.
func sudoPrompt(ctx context.Context) error {
	cmd := osexec.CommandContext(ctx, "sudo", "-v")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo authentication failed — RPM download requires sudo: %w", err)
	}
	return nil
}

func DownloadRPMs(ctx context.Context, packages []string, repoURL, bundleDir string, reposync, createrepo bool, exec executer.Executer) error {
	rpmsDir := filepath.Join(bundleDir, "rpms")
	if err := os.MkdirAll(rpmsDir, 0o755); err != nil {
		return fmt.Errorf("create rpms dir: %w", err)
	}

	logInfo("RPM download requires sudo — you may be prompted for your password.")
	if err := sudoPrompt(ctx); err != nil {
		return err
	}

	// If a repo URL is provided, download the .repo file and install it
	// temporarily into /etc/yum.repos.d/ so dnf can resolve packages from
	// the FlightCtl repo alongside the system repos.  The file is removed
	// when DownloadRPMs returns.
	const tempRepoPath = "/etc/yum.repos.d/flightctl-bundle-temp.repo"
	if repoURL != "" {
		localRepoFile, err := os.CreateTemp("", "flightctl-*.repo")
		if err != nil {
			return fmt.Errorf("create temp repo file: %w", err)
		}
		localRepoFile.Close()
		defer os.Remove(localRepoFile.Name())

		logInfo("Fetching repo file from %s...", repoURL)
		if err := fetchRepoFile(repoURL, localRepoFile.Name()); err != nil {
			return fmt.Errorf("fetch repo file from %s: %w", repoURL, err)
		}

		_, stderr, code := exec.ExecuteWithContext(ctx, "sudo", "-n", "cp", localRepoFile.Name(), tempRepoPath)
		if code != 0 {
			return fmt.Errorf("install temp repo file: %s", strings.TrimSpace(stderr))
		}
		defer func() {
			exec.ExecuteWithContext(ctx, "sudo", "-n", "rm", "-f", tempRepoPath) //nolint:errcheck
		}()
		logInfo("Repo file installed temporarily at %s", tempRepoPath)
	}

	if reposync {
		return downloadRPMsViaReposync(ctx, rpmsDir, exec)
	}
	return downloadRPMsViaDownload(ctx, packages, rpmsDir, createrepo, exec)
}

// downloadRPMsViaReposync mirrors the full FlightCtl RPM repository using
// `dnf reposync`.  The resulting layout is rpmsDir/flightctl/ containing
// all packages and repodata/ metadata suitable for use as a local dnf repo.
func downloadRPMsViaReposync(ctx context.Context, rpmsDir string, exec executer.Executer) error {
	logInfo("Mirroring full FlightCtl repository via dnf reposync (this may take several minutes)...")
	args := []string{"-n", "dnf", "reposync", "--repoid=flightctl", "--download-path", rpmsDir, "--download-metadata"}
	_, stderr, exitCode := exec.ExecuteWithContext(ctx, "sudo", args...)
	if exitCode != 0 {
		return fmt.Errorf("dnf reposync failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}
	logInfo("Repository mirrored to %s/flightctl/", rpmsDir)
	return nil
}

// downloadRPMsViaDownload fetches the specified packages and their transitive
// dependencies using `dnf download --resolve --alldeps`.  --alldeps ensures
// deps already installed on the prep machine are still downloaded (the target
// air-gapped machine may not have them).  --setopt=skip_if_unavailable=True
// prevents a hard failure when the prep machine has RHEL subscription repos
// configured that return 403 — community variants do not need Red Hat CDN.
// If createrepo is true, `createrepo_c` is run afterward to generate
// repodata/ so the directory can be used as a local dnf repository source.
func downloadRPMsViaDownload(ctx context.Context, packages []string, rpmsDir string, createrepo bool, exec executer.Executer) error {
	logInfo("Downloading RPMs: %s (this may take a few minutes...)", strings.Join(packages, ", "))
	args := []string{"-n", "dnf", "download", "--resolve", "--alldeps",
		"--setopt=skip_if_unavailable=True", "--destdir", rpmsDir}
	args = append(args, packages...)
	_, stderr, exitCode := exec.ExecuteWithContext(ctx, "sudo", args...)
	if exitCode != 0 {
		return fmt.Errorf("dnf download failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
	}
	logInfo("RPMs downloaded to %s", rpmsDir)

	if createrepo {
		logInfo("Generating repository metadata with createrepo_c...")
		_, stderr, exitCode = exec.ExecuteWithContext(ctx, "createrepo_c", rpmsDir)
		if exitCode != 0 {
			return fmt.Errorf("createrepo_c failed (exit %d): %s", exitCode, strings.TrimSpace(stderr))
		}
		logInfo("Repository metadata generated at %s/repodata/", rpmsDir)
	}

	return nil
}

// fetchRepoFile downloads a .repo file from url and writes it to destPath.
func fetchRepoFile(url, destPath string) error {
	resp, err := http.Get(url) //nolint:gosec // URL comes from a trusted CLI flag
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned HTTP %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// WriteImportScript writes an executable shell script to bundleDir/import.sh
// that imports all bundled images into a local container registry.
//
// The script contains one explicit skopeo copy command per image, using the
// dir: transport for the source and docker: for the destination.  Explicit
// commands avoid any directory-iteration ambiguity and preserve namespace paths.
func WriteImportScript(bundleDir string, pairs []ImagePair, variant string) error {
	scriptPath := filepath.Join(bundleDir, "import.sh")
	f, err := os.Create(scriptPath)
	if err != nil {
		return fmt.Errorf("create import script: %w", err)
	}
	defer f.Close()

	fmt.Fprintf(f, `#!/bin/bash
# FlightCtl Air-gap Image Import Script
# Variant:   %s
# Generated: %s
#
# Copies all bundled container images into a local registry.
#
# Usage:
#   ./import.sh [--registry host:port] [--tls]
#
# Options:
#   --registry host:port   Destination registry (default: localhost:5000)
#   --tls                  Enable TLS verification for the destination registry
#
# Prerequisites on the air-gapped machine:
#   - skopeo  (sudo dnf install -y skopeo)
#   - A running container registry reachable at the destination address

set -euo pipefail

REGISTRY="localhost:5000"
DEST_TLS_FLAG="--dest-tls-verify=false"

while [[ $# -gt 0 ]]; do
    case $1 in
        --registry) REGISTRY="$2"; shift 2 ;;
        --tls)      DEST_TLS_FLAG=""; shift ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGES_DIR="$SCRIPT_DIR/images"
TOTAL=%d
CURRENT=0

echo "Importing $TOTAL images to $REGISTRY..."
echo ""

`, variant, time.Now().UTC().Format(time.RFC3339), len(pairs))

	for _, p := range pairs {
		parts := strings.SplitN(p.Source, "/", 2)
		if len(parts) < 2 {
			continue
		}
		imageRelPath := parts[1]
		fmt.Fprintf(f, `CURRENT=$((CURRENT + 1))
echo "[$CURRENT/$TOTAL] %s"
skopeo copy $DEST_TLS_FLAG "dir:$IMAGES_DIR/%s" "docker://$REGISTRY/%s"

`, imageRelPath, imageRelPath, imageRelPath)
	}

	fmt.Fprintf(f, `echo ""
echo "Import complete: $TOTAL images imported to $REGISTRY"
`)

	if err := os.Chmod(scriptPath, 0o755); err != nil {
		return fmt.Errorf("chmod import script: %w", err)
	}

	logInfo("Import script written: import.sh")
	return nil
}

// WriteInstallRPMsScript writes bundleDir/install-rpms.sh, an executable
// script that installs all bundled RPMs using dnf.
//
// The generated script detects the RPM layout at runtime and selects the
// appropriate install strategy:
//
//   - reposync layout (rpms/flightctl/repodata/ exists): uses --repofrompath
//     to install named packages from the mirrored repo directory.
//   - createrepo layout (rpms/repodata/ exists): uses --repofrompath to
//     install named packages from the flat RPM directory with metadata.
//   - flat layout (neither repodata directory exists): generates repodata at
//     install time via createrepo_c (if available) then installs by name; falls
//     back to `rpm -Uvh --oldpackage` if createrepo_c is absent.  The *.rpm
//     glob with `dnf install` is intentionally avoided — it causes dnf to plan
//     a full transaction that can conflict with protected system packages.
//
// The packages slice is embedded in the script as the PACKAGES variable
// for use in the reposync and createrepo install paths.
func WriteInstallRPMsScript(bundleDir string, packages []string, reposync, createrepo bool) error {
	scriptPath := filepath.Join(bundleDir, "install-rpms.sh")
	f, err := os.Create(scriptPath)
	if err != nil {
		return fmt.Errorf("create install-rpms script: %w", err)
	}
	defer f.Close()

	// Build the success message based on the package set being installed.
	successMsg := installSuccessMessage(packages)

	fmt.Fprintf(f, `#!/bin/bash
# FlightCtl Air-gap RPM Installation Script
#
# Installs bundled RPMs from the rpms/ directory using dnf.
# Detects the RPM layout automatically:
#   - reposync layout:   rpms/flightctl/repodata/ exists
#   - createrepo layout: rpms/repodata/ exists
#   - flat layout:       generates repodata via createrepo_c (if available) then
#                        installs by package name; falls back to rpm -Uvh otherwise
#
# Usage: ./install-rpms.sh
# Requires: sudo

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RPMS_DIR="$SCRIPT_DIR/rpms"
PACKAGES=%q

if [ -z "$(ls -A "$RPMS_DIR" 2>/dev/null)" ]; then
    echo "No RPMs found in $RPMS_DIR" >&2
    exit 1
fi

echo "Installing RPMs from $RPMS_DIR..."

if [ -d "$RPMS_DIR/flightctl/repodata" ]; then
    # reposync layout: RPMs and metadata are in the repo subdirectory
    sudo dnf install -y --nogpgcheck --nobest \
        --disablerepo='*' \
        --repofrompath="flightctl-local,$RPMS_DIR/flightctl" \
        --enablerepo=flightctl-local \
        $PACKAGES
elif [ -d "$RPMS_DIR/repodata" ]; then
    # createrepo layout: flat RPMs with generated metadata
    sudo dnf install -y --nogpgcheck --nobest \
        --disablerepo='*' \
        --repofrompath="flightctl-local,$RPMS_DIR" \
        --enablerepo=flightctl-local \
        $PACKAGES
else
    # Flat layout — no repodata found.
    # Installing all *.rpm files via glob causes dnf to plan a transaction that
    # can conflict with protected system packages (e.g. systemd).  Generate repo
    # metadata at install time so dnf can install only the named packages instead.
    if command -v createrepo_c &>/dev/null; then
        echo "Generating repository metadata (createrepo_c)..."
        createrepo_c "$RPMS_DIR" >/dev/null 2>&1
        sudo dnf install -y --nogpgcheck --nobest \
            --disablerepo='*' \
            --repofrompath="flightctl-local,$RPMS_DIR" \
            --enablerepo=flightctl-local \
            $PACKAGES
    else
        echo "Warning: createrepo_c not found — falling back to rpm." >&2
        echo "To avoid this, install createrepo_c or rebuild the bundle with --rpm-createrepo." >&2
        sudo rpm -Uvh --oldpackage "$RPMS_DIR"/*.rpm
    fi
fi

echo ""
echo "RPM installation complete."
%s`, strings.Join(packages, " "), successMsg)

	if err := os.Chmod(scriptPath, 0o755); err != nil {
		return fmt.Errorf("chmod install-rpms script: %w", err)
	}

	logInfo("RPM install script written: install-rpms.sh")
	return nil
}

// installSuccessMessage returns the post-install guidance block for the
// install-rpms.sh script based on the packages being installed.
// flightctl-services takes priority: a bundle that includes both services and
// agent (agent RPM bundled for offline image building) should show server
// start instructions, not agent instructions.
func installSuccessMessage(packages []string) string {
	hasServices, hasAgent := false, false
	for _, p := range packages {
		switch p {
		case "flightctl-services":
			hasServices = true
		case "flightctl-agent":
			hasAgent = true
		}
	}
	if hasServices {
		return `echo "Enable and start the FlightCtl service:"
echo "  sudo systemctl enable --now flightctl.target"
echo "  sudo systemctl status flightctl.target"
`
	}
	if hasAgent {
		return `echo "Enable and start the FlightCtl agent:"
echo "  sudo systemctl enable --now flightctl-agent"
echo "  sudo systemctl status flightctl-agent"
`
	}
	return ""
}

// CreateArchive creates a gzip-compressed tar archive at destPath from all
// files under srcDir.  Archive entries are stored with paths relative to
// srcDir so the archive extracts cleanly into any target directory.
func CreateArchive(srcDir, destPath string) error {
	logInfo("Creating archive %s...", destPath)

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // skip srcDir itself
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}

		sf, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sf.Close()

		_, err = io.Copy(tw, sf)
		return err
	})

	if err != nil {
		return fmt.Errorf("walk bundle dir: %w", err)
	}

	logInfo("Archive created successfully.")
	return nil
}

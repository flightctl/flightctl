#!/usr/bin/env bash

# COPR to GitHub Pages RPM Download Script
# Downloads RPMs from COPR builds using copr-cli only

set -euo pipefail

# Configuration
COPR_PROJECT="@redhat-et/flightctl"
OUTPUT_DIR=".output"
DEST_DIR="$OUTPUT_DIR/copr-rpms-temp"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[INFO]${NC} $1" >&2; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Find COPR build for specific version
find_copr_build() {
    local version="$1"

    # Remove 'v' prefix if present
    version=${version#v}

    log "Searching for COPR build for version: $version"

    local builds
    builds=$(copr-cli list-builds "$COPR_PROJECT" --output-format json 2>/dev/null)
    [[ -n $builds ]] || {
        error "copr-cli returned no data – network/auth issue?"
        return 1
    }

    # Check recent successful builds
    for build_id in $(echo "$builds" | jq -r '.[] | select(.state == "succeeded") | .id' | head -20); do
        # Get build details via API
        local build_details
        build_details=$(curl -s "https://copr.fedorainfracloud.org/api_3/build/$build_id" 2>/dev/null || echo '{}')
        local build_version
        build_version=$(echo "$build_details" | jq -r '.source_package.version // empty' 2>/dev/null)

        if [ -n "$build_version" ] && [ "$build_version" != "null" ]; then
            # Remove build suffix from COPR version (everything after last dash)
            local copr_clean
            copr_clean=$(echo "$build_version" | sed 's/-[^-]*$//')

            log "  Checking build $build_id: version $copr_clean"

            # Direct match
            if [ "$version" = "$copr_clean" ]; then
                echo "$build_id"
                return 0
            fi

            # Handle pre-release versions (convert dashes to tildes)
            local version_tilde
            version_tilde=$(echo "$version" | sed 's/-rc/~rc/g' | sed 's/-alpha/~alpha/g' | sed 's/-beta/~beta/g')
            if [ "$version_tilde" = "$copr_clean" ]; then
                echo "$build_id"
                return 0
            fi
        fi
    done

    return 1
}

# Download chroot using copr-cli
download_chroot() {
    local build_id=$1
    local chroot=$2
    local dest_dir=$3

    log "Downloading $chroot..."

    if copr-cli download-build "$build_id" --dest "$dest_dir" --chroot "$chroot" 2>/dev/null; then
        success "Downloaded $chroot successfully"
        return 0
    else
        error "Failed to download $chroot"
        return 1
    fi
}

# Main execution
main() {
    local version="${1:-}"

    if [ -z "$version" ]; then
        error "Usage: $0 <version>"
        error "Example: $0 0.8.0"
        error "Example: $0 v0.8.0-rc2"
        exit 1
    fi

    log "Starting COPR download for version: $version"

    # Create output directory
    mkdir -p "$OUTPUT_DIR"

    # Find the build
    local build_id
    if ! build_id=$(find_copr_build "$version"); then
        error "Failed to find build for version $version"
        exit 1
    fi

    log "Using COPR build ID: $build_id"

    # Get available chroots
    local build_details
    build_details=$(curl -s "https://copr.fedorainfracloud.org/api_3/build/$build_id")
    [[ -n $build_details ]] || {
        error "Failed to fetch build details for build $build_id – network issue?"
        exit 1
    }
    local available_chroots
    available_chroots=$(echo "$build_details" | jq -r '.chroots[]')

    # Filter to only EPEL and Fedora chroots
    local -a filtered_chroots=()
    while IFS= read -r chroot; do
        if [[ "$chroot" == epel-* ]] || [[ "$chroot" == fedora-* ]]; then
            filtered_chroots+=("$chroot")
        fi
    done <<< "$available_chroots"

    if [ ${#filtered_chroots[@]} -eq 0 ]; then
        error "No EPEL or Fedora chroots found for build $build_id"
        exit 1
    fi

    log "Found chroots: ${filtered_chroots[*]}"

    # Clean destination directory
    rm -rf "$DEST_DIR"
    mkdir -p "$DEST_DIR"

    # Download chroots
    local success_count=0
    local total_count=0

    for chroot in "${filtered_chroots[@]}"; do
        total_count=$((total_count + 1))
        if download_chroot "$build_id" "$chroot" "$DEST_DIR"; then
            success_count=$((success_count + 1))
        fi
    done

    # Clean up unwanted RPMs
    log "Cleaning up packages..."
    find "$DEST_DIR" -name "*debuginfo*.rpm" -delete 2>/dev/null || true
    find "$DEST_DIR" -name "*debugsource*.rpm" -delete 2>/dev/null || true
    find "$DEST_DIR" -name "*.src.rpm" -delete 2>/dev/null || true

    # Keep only flightctl-agent and flightctl-cli
    find "$DEST_DIR" -name "*.rpm" ! -name "flightctl-agent-*.rpm" ! -name "flightctl-cli-*.rpm" -delete 2>/dev/null || true

    # Create repository metadata
    log "Creating repository metadata..."
    for chroot_dir in "$DEST_DIR"/*; do
        if [ -d "$chroot_dir" ]; then
            local chroot
            chroot=$(basename "$chroot_dir")
            createrepo_c "$chroot_dir" || {
                error "Failed to create repo metadata for $chroot"
                continue
            }
        fi
    done

    # Summary
    local total_rpms
    total_rpms=$(find "$DEST_DIR" -name "*.rpm" 2>/dev/null | wc -l)

    success "Download completed!"
    echo "  Successful chroots: $success_count/$total_count"
    echo "  Total RPMs: $total_rpms"
    echo "  Output directory: $DEST_DIR"
    echo ""
    echo "Next: Run create-rpm-repo.sh to generate the repository structure"
}

main "$@"

#!/usr/bin/env bash

# COPR to GitHub Pages RPM Upload Script
# This script finds COPR builds, downloads RPMs, and prepares them for GitHub Pages

set -euo pipefail

# Configuration
COPR_PROJECT="@redhat-et/flightctl"
DEST_DIR="copr-rpms-temp"
PARALLEL_JOBS=4
MAX_RETRIES=3

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $1"
}

success() {
    echo -e "${GREEN}✅${NC} $1"
}

warning() {
    echo -e "${YELLOW}⚠️${NC} $1"
}

error() {
    echo -e "${RED}❌${NC} $1"
}

# Find COPR build for specific version - CLEAN VERSION WITHOUT LOGGING
find_copr_build() {
    local version="$1"
    local builds=$(copr-cli list-builds "$COPR_PROJECT" --output-format json 2>/dev/null)

    # Remove 'v' prefix if present
    version=${version#v}

    # Check recent successful builds
    for build_id in $(echo "$builds" | jq -r '.[] | select(.state == "succeeded") | .id' | head -10); do
        # Get build details via API
        local build_details=$(curl -s "https://copr.fedorainfracloud.org/api_3/build/$build_id" 2>/dev/null || echo '{}')
        local build_version=$(echo "$build_details" | jq -r '.source_package.version // empty' 2>/dev/null)

        if [ -n "$build_version" ] && [ "$build_version" != "null" ]; then
            # Remove build suffix from COPR version (everything after last dash)
            local copr_clean=$(echo "$build_version" | sed 's/-[^-]*$//')

            # Direct match
            if [ "$version" = "$copr_clean" ]; then
                echo "$build_id"
                return 0
            fi

            # Handle pre-release versions
            local version_tilde=$(echo "$version" | sed 's/-rc/~rc/g' | sed 's/-alpha/~alpha/g' | sed 's/-beta/~beta/g')
            if [ "$version_tilde" = "$copr_clean" ]; then
                echo "$build_id"
                return 0
            fi
        fi
    done

    return 1
}

# Download function using curl (faster for multiple files)
download_chroot_curl() {
    local repo_url=$1
    local chroot=$2
    local dest_dir=$3

    echo "[$chroot] 🚀 Starting curl download..."
    local start_time=$(date +%s)

    local chroot_url="$repo_url/$chroot/"
    local chroot_dir="$dest_dir/$chroot"
    mkdir -p "$chroot_dir"

    echo "[$chroot] Getting file list..."
    local file_list=$(timeout 60 curl -s --connect-timeout 15 "$chroot_url" 2>/dev/null | grep -o 'href="[^"]*\.rpm"' | sed 's/href="//g' | sed 's/"//g' 2>/dev/null)

    if [ -z "$file_list" ]; then
        echo "[$chroot] ❌ No files found or timeout"
        return 1
    fi

    # Filter out debug packages and source RPMs
    local filtered_files=$(echo "$file_list" | grep -v debuginfo | grep -v debugsource | grep -v '\.src\.rpm$')
    local total_files=$(echo "$filtered_files" | wc -l)

    echo "[$chroot] Found $total_files RPM files (excluding debug)"

    # Use parallel to download files
    echo "$filtered_files" | parallel -j"$PARALLEL_JOBS" --will-cite "
        rpm_file={}
        file_url=\"$chroot_url\$rpm_file\"
        local_file=\"$chroot_dir/\$rpm_file\"

        # Download with retries
        for retry in {1..$MAX_RETRIES}; do
            if curl -f -L --connect-timeout 20 --max-time 180 -o \"\$local_file\" \"\$file_url\" 2>/dev/null; then
                echo \"[$chroot]   ✅ \$rpm_file\"
                exit 0
            else
                if [ \$retry -eq $MAX_RETRIES ]; then
                    echo \"[$chroot]   ❌ Failed: \$rpm_file\"
                fi
                sleep 2
            fi
        done
        exit 1
    "

    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    local final_count=$(find "$chroot_dir" -name "*.rpm" | wc -l)

    echo "[$chroot] ✅ Complete in ${duration}s: $final_count RPMs downloaded"
    return 0
}

# Download function using copr-cli (more reliable but slower)
download_chroot_copr_cli() {
    local build_id=$1
    local chroot=$2
    local dest_dir=$3

    echo "[$chroot] 🔄 Starting copr-cli download..."
    local start_time=$(date +%s)

    if timeout 1200 copr-cli download-build "$build_id" --dest "$dest_dir" --chroot "$chroot" 2>/dev/null; then
        local end_time=$(date +%s)
        local duration=$((end_time - start_time))
        echo "[$chroot] ✅ copr-cli success in ${duration}s"
        return 0
    else
        echo "[$chroot] ❌ copr-cli failed"
        return 1
    fi
}

# Hybrid download approach - try curl first, fallback to copr-cli
download_chroot_hybrid() {
    local build_id=$1
    local chroot=$2
    local dest_dir=$3
    local repo_url=$4
    local use_curl=${5:-true}

    echo "[$chroot] Starting download..."

    if [ "$use_curl" = true ]; then
        if download_chroot_curl "$repo_url" "$chroot" "$dest_dir"; then
            return 0
        else
            echo "[$chroot] ⚠️  curl failed, trying copr-cli..."
        fi
    fi

    download_chroot_copr_cli "$build_id" "$chroot" "$dest_dir"
}

# Create repository metadata
create_repo_metadata() {
    local dest_dir="$1"

    log "Creating repository metadata..."

    for chroot_dir in "$dest_dir"/*; do
        if [ -d "$chroot_dir" ]; then
            local chroot=$(basename "$chroot_dir")
            echo "  Creating metadata for $chroot..."

            # Create repodata
            createrepo_c "$chroot_dir" || {
                warning "Failed to create repo metadata for $chroot"
                continue
            }

            # Create simple index.html for browsing
            cat > "$chroot_dir/index.html" << EOF
<!DOCTYPE html>
<html>
<head>
    <title>FlightCtl RPMs - $chroot</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; }
        .rpm-list { list-style-type: none; }
        .rpm-list li { margin: 5px 0; }
        .rpm-list a { text-decoration: none; color: #0066cc; }
        .rpm-list a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <h1>FlightCtl RPM Repository - $chroot</h1>
    <p>Available RPM packages:</p>
    <ul class="rpm-list">
EOF

            # Add RPM files to index
            find "$chroot_dir" -name "*.rpm" -printf "%f\n" | sort | while read rpm; do
                echo "        <li><a href=\"$rpm\">$rpm</a></li>" >> "$chroot_dir/index.html"
            done

            cat >> "$chroot_dir/index.html" << EOF
    </ul>
    <hr>
    <p><small>Generated on $(date)</small></p>
</body>
</html>
EOF
        fi
    done
}

# Main execution function
main() {
    local version="${1:-}"
    local use_curl="${2:-true}"

    if [ -z "$version" ]; then
        error "Usage: $0 <version> [use_curl=true]"
        error "Example: $0 0.8.0"
        error "Example: $0 v0.8.0-rc2 false"
        exit 1
    fi

    log "Starting COPR to GitHub Pages download for version: $version"

    # Find the build
    log "Searching for COPR build..."
    local build_id
    build_id=$(find_copr_build "$version")

    if [ $? -ne 0 ] || [ -z "$build_id" ]; then
        error "Failed to find build for version $version"
        exit 1
    fi

    log "Using COPR build ID: $build_id"

    # Get build details
    log "Getting build details for build $build_id..."
    local build_details=$(curl -s "https://copr.fedorainfracloud.org/api_3/build/$build_id")

    local repo_url=$(echo "$build_details" | jq -r '.repo_url // empty')
    local available_chroots=$(echo "$build_details" | jq -r '.chroots[]')

    if [ -z "$repo_url" ]; then
        error "Failed to get repository URL for build $build_id"
        exit 1
    fi

    log "Repository URL: $repo_url"
    log "Available chroots:"
    echo "$available_chroots" | while read chroot; do
        echo "  - $chroot"
    done

    # Clean destination directory
    rm -rf "$DEST_DIR"
    mkdir -p "$DEST_DIR"

    # Download all chroots in parallel
    echo -e "\n📋 Per-chroot breakdown:"
    echo "$available_chroots" | nl

    log "Starting parallel downloads..."
    echo "$available_chroots" | parallel -j2 --will-cite \
        "download_chroot_hybrid '$build_id' {} '$DEST_DIR' '$repo_url' '$use_curl'"

    # Clean up unwanted RPMs
    log "Cleaning up debug and source RPMs..."
    echo "Removing debuginfo, debugsource, and source RPMs..."
    find "$DEST_DIR" -name "*debuginfo*.rpm" -delete 2>/dev/null || true
    find "$DEST_DIR" -name "*debugsource*.rpm" -delete 2>/dev/null || true
    find "$DEST_DIR" -name "*.src.rpm" -delete 2>/dev/null || true

    # Report cleanup results
    local remaining_rpms=$(find "$DEST_DIR" -name "*.rpm" | wc -l)
    log "Cleanup complete. $remaining_rpms RPMs remaining."

    # Create repository metadata
    create_repo_metadata "$DEST_DIR"

    # Summary
    echo -e "\n📊 Download Summary:"
    local total_rpms=0
    for chroot_dir in "$DEST_DIR"/*; do
        if [ -d "$chroot_dir" ]; then
            local chroot=$(basename "$chroot_dir")
            local rpm_count=$(find "$chroot_dir" -name "*.rpm" | wc -l)
            total_rpms=$((total_rpms + rpm_count))
            echo "  $chroot: $rpm_count RPMs"
        fi
    done

    echo -e "\n📦 Final repository contents:"
    echo "  Total RPMs downloaded: $total_rpms"
    echo "  Storage location: $DEST_DIR"

    success "COPR download complete! Ready for GitHub Pages upload."
    echo ""
    echo "Next steps:"
    echo "1. Copy contents of $DEST_DIR to your GitHub Pages repository"
    echo "2. Commit and push to publish the RPM repository"
    echo "3. Users can install with: dnf config-manager --add-repo <your-github-pages-url>"
}

# Export functions for parallel to use
export -f download_chroot_curl
export -f download_chroot_copr_cli
export -f download_chroot_hybrid

# Run main function
main "$@"

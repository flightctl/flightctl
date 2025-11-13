#!/bin/bash

# Script to get release versions based on --back parameter
# Usage: get-release-version.sh --back=n
# where n=0 gets current version, n=1 gets previous release, etc.

set -euo pipefail

# Default values
BACK=0
SCRIPT_NAME=$(basename "$0")

# Function to show usage
usage() {
    cat << EOF
Usage: $SCRIPT_NAME --back=N

Get release version based on how many releases back to look.

Options:
    --back=N    Number of releases to go back (default: 0)
                N=0: Current/latest release version
                N=1: Previous release version
                N=2: Two releases back, etc.

Examples:
    $SCRIPT_NAME --back=0    # Get latest release
    $SCRIPT_NAME --back=1    # Get previous release
    $SCRIPT_NAME --back=2    # Get two releases back

Note: Only considers semantic version tags (vX.Y.Z) and release candidates (vX.Y.Z-rcN)
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --back=*)
            BACK="${1#*=}"
            if ! [[ "$BACK" =~ ^[0-9]+$ ]]; then
                echo "Error: --back value must be a non-negative integer" >&2
                exit 1
            fi
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Error: Unknown option $1" >&2
            usage >&2
            exit 1
            ;;
    esac
    shift
done

# Check if we're in a git repository
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo "Error: Not in a git repository" >&2
    exit 1
fi

# Get all tags that match semantic versioning pattern (with optional -rc suffix)
# Pattern matches: v1.2.3, v1.2.3-rc1, v1.2.3-rc10, etc.
# Sort by version number (reverse order to get latest first)
get_release_tags() {
    git tag -l | \
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$' | \
    sort -V -r
}

# Get the Nth release version (0-indexed)
get_version_at_index() {
    local index=$1
    local tags

    # Get sorted tags into array
    mapfile -t tags < <(get_release_tags)

    # Check if we have enough tags
    if [[ ${#tags[@]} -eq 0 ]]; then
        echo "Error: No release tags found" >&2
        exit 1
    fi

    if [[ $index -ge ${#tags[@]} ]]; then
        echo "Error: Not enough release tags. Found ${#tags[@]} tags, but requested index $index" >&2
        echo "Available tags:" >&2
        printf '  %s\n' "${tags[@]}" >&2
        exit 1
    fi

    echo "${tags[$index]}"
}

# Main execution
version=$(get_version_at_index "$BACK")
echo "$version"
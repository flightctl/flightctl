#!/bin/bash
set -euo pipefail

# Load GitHub Actions artifact into podman
# Usage:
#   load-github-artifact.sh <artifact_id>
#   load-github-artifact.sh <artifact_name> [run_id] [repo]

usage() {
    echo "Usage:"
    echo "  $0 <artifact_id>                    # Direct artifact ID"
    echo "  $0 <artifact_name> [run_id] [repo] # Find by name"
    echo ""
    echo "Environment variables (used if params not provided):"
    echo "  GITHUB_RUN_ID    - GitHub Actions run ID"
    echo "  GITHUB_REPOSITORY - Repository (owner/repo format)"
}

# Parse arguments
if [[ $# -eq 0 ]] || [[ "$1" == "-h" ]] || [[ "$1" == "--help" ]]; then
    usage
    exit 0
fi

ARTIFACT_NAME_OR_ID="$1"
RUN_ID="${2:-${GITHUB_RUN_ID:-}}"
REPO="${3:-${GITHUB_REPOSITORY:-}}"

# Check if first argument looks like an artifact ID (numeric)
if [[ "$ARTIFACT_NAME_OR_ID" =~ ^[0-9]+$ ]]; then
    # Direct artifact ID
    ARTIFACT_ID="$ARTIFACT_NAME_OR_ID"
    echo "Using artifact ID: $ARTIFACT_ID"
else
    # Artifact name - need run ID and repo
    ARTIFACT_NAME="$ARTIFACT_NAME_OR_ID"

    if [[ -z "$RUN_ID" ]]; then
        echo "Error: Run ID required when using artifact name (provide as argument or set GITHUB_RUN_ID)"
        exit 1
    fi

    if [[ -z "$REPO" ]]; then
        echo "Error: Repository required when using artifact name (provide as argument or set GITHUB_REPOSITORY)"
        exit 1
    fi

    echo "Looking up artifact '$ARTIFACT_NAME' in run $RUN_ID from $REPO"

    # Find artifact ID by name (use high per_page to get all artifacts)
    ARTIFACT_ID=$(gh api "/repos/${REPO}/actions/runs/${RUN_ID}/artifacts?per_page=100" \
        --jq ".artifacts[] | select(.name==\"${ARTIFACT_NAME}\").id")

    if [[ -z "$ARTIFACT_ID" ]]; then
        echo "Error: Artifact '$ARTIFACT_NAME' not found in run $RUN_ID"
        exit 1
    fi

    echo "Found artifact ID: $ARTIFACT_ID"
fi

# Download and load into podman
echo "Downloading and loading artifact into podman..."

gh api "/repos/${REPO:-flightctl/flightctl}/actions/artifacts/${ARTIFACT_ID}/zip" \
    | funzip \
    | podman load

echo "✓ Artifact loaded successfully into podman"
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

    # Find artifacts by name (use high per_page to get all artifacts)
    ARTIFACTS_JSON=$(gh api "/repos/${REPO}/actions/runs/${RUN_ID}/artifacts?per_page=100")
    MATCHES_JSON=$(echo "$ARTIFACTS_JSON" | jq --arg name "$ARTIFACT_NAME" '[.artifacts[] | select(.name==$name)]')
    MATCH_COUNT=$(echo "$MATCHES_JSON" | jq 'length')

    if [[ "$MATCH_COUNT" -eq 0 ]]; then
        echo "Error: Artifact '$ARTIFACT_NAME' not found in run $RUN_ID"
        exit 1
    elif [[ "$MATCH_COUNT" -eq 1 ]]; then
        ARTIFACT_ID=$(echo "$MATCHES_JSON" | jq -r '.[0].id')
        echo "Found artifact ID: $ARTIFACT_ID"
    else
        DIGESTS_UNIQUE_COUNT=$(echo "$MATCHES_JSON" | jq '[.[].digest // ""] | unique | length')
        if [[ "$DIGESTS_UNIQUE_COUNT" -gt 1 ]]; then
            echo "Error: Multiple artifacts named '$ARTIFACT_NAME' found in run $RUN_ID with differing digests:"
            echo "$MATCHES_JSON" | jq -r '.[] | "  id=\(.id) digest=\(.digest // "null") created_at=\(.created_at)"'
            exit 1
        fi
        ARTIFACT_ID=$(echo "$MATCHES_JSON" | jq -r '.[0].id')
        SHARED_DIGEST=$(echo "$MATCHES_JSON" | jq -r '.[0].digest // ""')
        echo "Note: Found $MATCH_COUNT artifacts named '$ARTIFACT_NAME' with identical digest '$SHARED_DIGEST'. Using artifact ID: $ARTIFACT_ID"
    fi
fi

# Download and load into podman
echo "Downloading and loading artifact into podman..."

gh api "/repos/${REPO:-flightctl/flightctl}/actions/artifacts/${ARTIFACT_ID}/zip" \
    | funzip \
    | podman load

echo "✓ Artifact loaded successfully into podman"
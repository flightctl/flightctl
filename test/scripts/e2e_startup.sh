#!/usr/bin/env bash
set -euo pipefail

# Get the project root directory (where the bin/ directory is located)
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FLIGHTCTL_BIN="${PROJECT_ROOT}/bin/flightctl"

# Check if flightctl binary exists
if [[ ! -x "${FLIGHTCTL_BIN}" ]]; then
    echo "❌ [Startup] Error: flightctl binary not found at ${FLIGHTCTL_BIN}"
    echo "    Please run 'make build-cli' first to build the flightctl CLI"
    exit 1
fi

echo "🔄 [Startup] Starting global E2E environment setup..."

# Resource types to clean up (in order of dependencies)
RESOURCE_TYPES=(
    "resourcesync"
    "fleet"
    "device"
    "enrollmentrequest"
    "repository"
    "certificatesigningrequest"
)

# Function to clean up resources of a specific type
cleanup_resource_type() {
    local resource_type="$1"
    echo "🔄 [Startup] Cleaning up ${resource_type} resources..."
    
    # Get all resources of this type
    local resources
    if ! resources=$("${FLIGHTCTL_BIN}" get "$resource_type" -o name 2>/dev/null); then
        echo "⚠️  [Startup] Warning: failed to get $resource_type resources (API may not be ready)"
        return 0
    fi
    
    resources=$(echo "$resources" | tr -d '\r' | grep -v '^$' || true)
    
    if [[ -z "$resources" ]]; then
        echo "ℹ️  [Startup] No $resource_type resources found to delete"
        return 0
    fi
    
    echo "🔍 [Startup] Found $resource_type resources to delete:"
    echo "$resources"
    
    # Delete the resources
    if ! echo "$resources" | xargs "${FLIGHTCTL_BIN}" delete "$resource_type" 2>/dev/null; then
        echo "⚠️  [Startup] Warning: failed to delete some $resource_type resources"
        return 0
    fi
    
    echo "✅ [Startup] Successfully deleted $resource_type resources"
}

# Clean up all resource types
for resource_type in "${RESOURCE_TYPES[@]}"; do
    cleanup_resource_type "$resource_type"
done

echo "✅ [Startup] Global E2E environment setup completed"
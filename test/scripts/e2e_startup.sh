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

# Ensure CLI is configured with correct endpoint
# API_ENDPOINT should be set by run_e2e_tests.sh or environment
if [[ -n "${API_ENDPOINT:-}" ]]; then
    echo "🔄 [Startup] Using API endpoint: ${API_ENDPOINT}"
    # Try to login/configure the CLI if not already configured
    # Use --insecure-skip-tls-verify for self-signed certs
    if ! "${FLIGHTCTL_BIN}" get device -o name &>/dev/null; then
        echo "🔄 [Startup] CLI not configured, attempting login..."
        
        # Check if we're in Quadlet/PAM mode
        if [[ "${E2E_ENVIRONMENT:-}" == "quadlet" ]]; then
            # Set up PAM admin user if needed
            echo "🔄 [Startup] Setting up PAM admin user for Quadlet environment..."
            PAM_USER="${E2E_PAM_USER:-admin}"
            PAM_PASS="${E2E_PAM_PASSWORD:-${E2E_DEFAULT_PAM_PASSWORD:-flightctl-e2e}}"
            
            # Check if PAM issuer container is running
            if sudo podman ps --format '{{.Names}}' 2>/dev/null | grep -q '^flightctl-pam-issuer$'; then
                # Create flightctl-admin group if needed
                sudo podman exec -i flightctl-pam-issuer groupadd flightctl-admin 2>/dev/null || true
                
                # Create user if needed
                if ! sudo podman exec flightctl-pam-issuer id "${PAM_USER}" &>/dev/null; then
                    sudo podman exec flightctl-pam-issuer adduser "${PAM_USER}" 2>/dev/null || true
                fi
                
                # Set password
                sudo podman exec -i flightctl-pam-issuer sh -c "echo '${PAM_USER}:${PAM_PASS}' | chpasswd" 2>/dev/null || true
                
                # Add to flightctl-admin group
                sudo podman exec -i flightctl-pam-issuer usermod -aG flightctl-admin "${PAM_USER}" 2>/dev/null || true
                
                echo "✅ [Startup] PAM admin user '${PAM_USER}' configured"
            else
                echo "⚠️  [Startup] Warning: flightctl-pam-issuer container not running"
            fi
            
            # Login with PAM credentials
            if ! "${FLIGHTCTL_BIN}" login "${API_ENDPOINT}" --insecure-skip-tls-verify -u "${PAM_USER}" -p "${PAM_PASS}" &>/dev/null; then
                echo "⚠️  [Startup] Warning: Could not login to API at ${API_ENDPOINT} with PAM user '${PAM_USER}'"
                echo "    Tests may fail if CLI is not configured."
            else
                echo "✅ [Startup] CLI login successful with PAM user '${PAM_USER}'"
            fi
        else
            # Non-Quadlet environment - try no-auth login
            if ! "${FLIGHTCTL_BIN}" login "${API_ENDPOINT}" --insecure-skip-tls-verify &>/dev/null; then
                echo "⚠️  [Startup] Warning: Could not login to API at ${API_ENDPOINT}"
                echo "    Tests may fail if CLI is not configured. You may need to login manually."
            else
                echo "✅ [Startup] CLI login successful"
            fi
        fi
    fi
else
    echo "⚠️  [Startup] Warning: API_ENDPOINT not set, using CLI default configuration"
fi

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
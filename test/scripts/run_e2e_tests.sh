#!/usr/bin/env bash
set -x -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

REPORTS=${1}
GO_E2E_DIRS=("${@:2}")
GINKGO_FOCUS=${GINKGO_FOCUS:-""}
#Filtering e2e tests by labels
GINKGO_LABEL_FILTER=${GINKGO_LABEL_FILTER:-""}
#Parallel execution - default to 1 but can be overridden
GINKGO_PROCS=${GINKGO_PROCS:-1}
#Output interceptor mode for parallel execution - 'dup' shows output from all nodes
GINKGO_OUTPUT_INTERCEPTOR_MODE=${GINKGO_OUTPUT_INTERCEPTOR_MODE:-"dup"}
# Manual test splitting variables
GINKGO_TOTAL_NODES=${GINKGO_TOTAL_NODES:-1}
GINKGO_NODE=${GINKGO_NODE:-1}
# Discovery control variables
DISCOVERY_PATH=${DISCOVERY_PATH:-"discovery.json"}
DISCOVERY_ONLY=${DISCOVERY_ONLY:-""}
FOCUS_FLAG=""


go install github.com/onsi/ginkgo/v2/ginkgo

GOBIN=$(go env GOBIN)
if [ -z "$GOBIN" ]; then
    GOBIN=$(go env GOPATH)/bin
fi

# Run discovery and save to file
run_discovery() {
    echo "Running test discovery..."
    DISCOVER_CMD=("${GOBIN}/ginkgo" "run" "--dry-run" "--json-report" "${DISCOVERY_PATH}")

    if [[ -n "${GINKGO_FOCUS}" ]]; then
        DISCOVER_CMD+=("--focus" "${GINKGO_FOCUS}")
    fi

    if [[ -n "${GINKGO_LABEL_FILTER}" ]]; then
        DISCOVER_CMD+=("--label-filter" "${GINKGO_LABEL_FILTER}")
    fi

    DISCOVER_CMD+=("${GO_E2E_DIRS[@]}")

    # Run discovery - fail if ginkgo fails (compilation errors, etc.)
    if ! "${DISCOVER_CMD[@]}"; then
        echo "ERROR: Test discovery failed!"
        exit 1
    fi
    echo "Discovery saved to: ${DISCOVERY_PATH}"
}

# Discovery-only mode: run discovery and exit (no cluster required)
if [[ -n "${DISCOVERY_ONLY}" ]]; then
    echo "Discovery-only mode enabled"
    run_discovery
    exit 0
fi

# Determine environment type and set API_ENDPOINT accordingly
E2E_ENVIRONMENT=${E2E_ENVIRONMENT:-""}

# Auto-detect environment if not set
if [[ -z "${E2E_ENVIRONMENT}" ]]; then
    if kubectl config current-context &>/dev/null; then
        E2E_ENVIRONMENT="k8s"
    elif systemctl is-active flightctl-api.service &>/dev/null || sudo systemctl is-active flightctl-api.service &>/dev/null; then
        E2E_ENVIRONMENT="quadlet"
    else
        E2E_ENVIRONMENT="k8s"  # Default to k8s
    fi
    echo "Auto-detected E2E_ENVIRONMENT: ${E2E_ENVIRONMENT}"
fi
export E2E_ENVIRONMENT

# Set API_ENDPOINT based on environment
if [[ -z "${API_ENDPOINT:-}" ]]; then
    case "${E2E_ENVIRONMENT}" in
        quadlet)
            # For Quadlet, use FQDN so VMs can reach the API
            QUADLET_HOST=$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo "localhost")
            API_ENDPOINT="https://${QUADLET_HOST}:3443"
            echo "Using Quadlet API endpoint: ${API_ENDPOINT}"
            ;;
        *)
            # For K8s environments, get endpoint from route/ingress
            API_ENDPOINT=https://$(get_endpoint_host flightctl-api-route)
            echo "Using K8s API endpoint: ${API_ENDPOINT}"
            ;;
    esac
fi
export API_ENDPOINT

# Set registry endpoint (K8s-specific, optional for Quadlet)
if [[ "${E2E_ENVIRONMENT}" != "quadlet" ]]; then
    REGISTRY_ENDPOINT=$(registry_address)
    export REGISTRY_ENDPOINT
else
    # For Quadlet, registry may not be needed or use a local one
    REGISTRY_ENDPOINT=${REGISTRY_ENDPOINT:-""}
    if [[ -n "${REGISTRY_ENDPOINT}" ]]; then
        export REGISTRY_ENDPOINT
    fi
fi

# Set PAM authentication credentials for Quadlet environments
if [[ "${E2E_ENVIRONMENT}" == "quadlet" ]]; then
    # Default PAM admin user credentials for E2E tests
    # These can be overridden by setting E2E_PAM_USER and E2E_PAM_PASSWORD
    export E2E_PAM_USER="${E2E_PAM_USER:-admin}"
    export E2E_DEFAULT_PAM_PASSWORD="${E2E_DEFAULT_PAM_PASSWORD:-flightctl-e2e}"
    echo "PAM credentials configured for user: ${E2E_PAM_USER}"
fi

# Handle manual test splitting if enabled
if [[ "${GINKGO_TOTAL_NODES}" -gt 1 ]]; then
    echo "Manual test splitting enabled: Node ${GINKGO_NODE} of ${GINKGO_TOTAL_NODES}"

    TEMP_TEST_LIST=$(mktemp)
    DISCOVERY_GENERATED=false

    # Use existing discovery file or run discovery
    if [[ -f "${DISCOVERY_PATH}" ]]; then
        echo "Loading discovery from existing file: ${DISCOVERY_PATH}"
    else
        echo "Discovery file not found, running discovery..."
        run_discovery
        DISCOVERY_GENERATED=true
    fi

    # Parse the JSON report to extract test names that would actually run
    # Use jq to extract just the LeafNodeText (test description) for focus patterns
    # Sort and deduplicate to ensure consistent distribution
    # Filter for tests that would run (not skipped) and are actual test specs (LeafNodeType == "It")
    jq -r '
        .[] |
        .SpecReports[]? |
        select(.LeafNodeType == "It" and .State != "skipped") |
        .LeafNodeText
    ' "${DISCOVERY_PATH}" | LC_ALL=C sort -u > "${TEMP_TEST_LIST}"

    # Clean up the discovery file only if we generated it
    if [[ "${DISCOVERY_GENERATED}" == "true" ]]; then
        rm -f "${DISCOVERY_PATH}"
    fi

    # Count total tests
    TOTAL_TESTS=$(wc -l < "${TEMP_TEST_LIST}")
    echo "Total tests found: ${TOTAL_TESTS}"

    # Extract tests for this specific node using awk
    NODE_TESTS=$(mktemp)
    awk -v node="${GINKGO_NODE}" -v total="${GINKGO_TOTAL_NODES}" 'NR % total == node - 1' "${TEMP_TEST_LIST}" > "${NODE_TESTS}"

    # Count tests for this node
    NODE_TEST_COUNT=$(wc -l < "${NODE_TESTS}")
    echo "Tests for node ${GINKGO_NODE}: ${NODE_TEST_COUNT}"

    # Check if this node has any tests to run
    if [[ "${NODE_TEST_COUNT}" -eq 0 ]]; then
        echo "No tests assigned to node ${GINKGO_NODE}. Skipping execution."
        rm -f "${TEMP_TEST_LIST}" "${NODE_TESTS}"
        exit 0
    fi

    # Display which tests this node will run
    echo "Node ${GINKGO_NODE} will run the following tests:"
    cat "${NODE_TESTS}"

    # Combine all tests for this node into a single focus pattern
    # Use regex OR (|) to match any of the tests
    if [[ -s "${NODE_TESTS}" ]]; then
        # Read all tests, escape regex metacharacters, and join them with | for regex OR
        FOCUS_PATTERN=$(sed 's/[[\.*^$()+?{|\\]/\\&/g' "${NODE_TESTS}" | paste -sd '|')
        echo "Focus pattern for node ${GINKGO_NODE}: ${FOCUS_PATTERN}"
        GINKGO_FOCUS="${FOCUS_PATTERN}"
    fi

    # Clean up temporary files
    rm -f "${TEMP_TEST_LIST}" "${NODE_TESTS}"
fi

# Build the ginkgo command using guard patterns for each flag
CMD=("${GOBIN}/ginkgo" "run")

if [[ -n "${GINKGO_FOCUS}" ]]; then
    CMD+=("--focus" "${GINKGO_FOCUS}")
fi

if [[ -n "${GINKGO_LABEL_FILTER}" ]]; then
    CMD+=("--label-filter" "${GINKGO_LABEL_FILTER}")
fi

# Add standard flags
CMD+=(--timeout 120m --race -vv -nodes="${GINKGO_PROCS}" --show-node-events --trace --force-newlines --output-interceptor-mode "${GINKGO_OUTPUT_INTERCEPTOR_MODE}" --github-output --output-dir "${REPORTS}" --junit-report junit_e2e_test.xml)

# Add progress polling flags for parallel execution
if [[ "${GINKGO_PROCS}" -gt 1 ]]; then
    CMD+=(--poll-progress-after=2m --poll-progress-interval=30s)
fi

# Add the test directories last
CMD+=("${GO_E2E_DIRS[@]}")

echo "Running e2e tests with ${GINKGO_PROCS} parallel processes..."
echo "Output interceptor mode: ${GINKGO_OUTPUT_INTERCEPTOR_MODE} (dup=show all output, swap=clean output)"
echo "Reports will be saved to: ${REPORTS}"

# Step 1: Run startup
echo "🔄 [Test Execution] Step 1: Running startup..."
if ! test/scripts/e2e_startup.sh; then
    echo "❌ [Test Execution] Startup failed, exiting"
    exit 1
fi
echo "✅ [Test Execution] Startup completed successfully"

# Step 2: Run the tests
echo "🔄 [Test Execution] Step 2: Running tests..."
TEST_EXIT_CODE=0
"${CMD[@]}" || TEST_EXIT_CODE=$?

# Step 3: Run cleanup (always run, even if tests failed)
echo "🔄 [Test Execution] Step 3: Running cleanup..."
if ! test/scripts/e2e_cleanup.sh; then
    echo "⚠️  [Test Execution] Cleanup failed, but continuing..."
fi
echo "✅ [Test Execution] Cleanup completed"

# Step 3b: Re-run the same cluster resource cleanup as startup
echo "🔄 [Test Execution] Step 3b: Running post-test resource cleanup (same logic as e2e_startup.sh)..."
if ! test/scripts/e2e_startup.sh; then
    echo "⚠️  [Test Execution] Post-test resource cleanup failed, but continuing to report test result..."
else
    echo "✅ [Test Execution] Post-test resource cleanup completed successfully"
fi

# Exit with the test exit code
exit $TEST_EXIT_CODE
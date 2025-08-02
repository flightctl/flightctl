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
FOCUS_FLAG=""


go install github.com/onsi/ginkgo/v2/ginkgo

GOBIN=$(go env GOBIN)

export API_ENDPOINT=https://$(get_endpoint_host flightctl-api-route)
export REGISTRY_ENDPOINT=$(registry_address)

# If we're doing manual test splitting (GINKGO_TOTAL_NODES > 1)
if [[ "${GINKGO_TOTAL_NODES}" -gt 1 ]]; then
    echo "Manual test splitting enabled: Node ${GINKGO_NODE} of ${GINKGO_TOTAL_NODES}"
    
    # Generate a list of all tests that would run
    echo "Generating list of all tests..."
    TEMP_TEST_LIST=$(mktemp)
    
    # Build the base ginkgo command to discover tests
    DISCOVER_CMD=("${GOBIN}/ginkgo" "run" "--dry-run" "--json-report" "discovery.json")
    
    if [[ -n "${GINKGO_FOCUS}" ]]; then
        DISCOVER_CMD+=("--focus" "${GINKGO_FOCUS}")
    fi
    
    if [[ -n "${GINKGO_LABEL_FILTER}" ]]; then
        DISCOVER_CMD+=("--label-filter" "${GINKGO_LABEL_FILTER}")
    fi
    
    DISCOVER_CMD+=("${GO_E2E_DIRS[@]}")
    
    # Run the discovery command and generate JSON report
    # We ignore the exit code because some test suites might have issues, but we still want to parse the JSON
    "${DISCOVER_CMD[@]}" > /dev/null 2>&1 || true
    
    # Parse the JSON report to extract test names with sanity label
    # Use jq to extract just the LeafNodeText (test description) for focus patterns
    # Sort and deduplicate to ensure consistent distribution
    jq -r '
        .[] | 
        .SpecReports[]? | 
        select(.LeafNodeLabels != null and (.LeafNodeLabels | contains(["sanity"]))) |
        .LeafNodeText
    ' discovery.json | sort -u > "${TEMP_TEST_LIST}"
    
    # Clean up the JSON file
    rm -f discovery.json
    
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
    
    # Build the ginkgo command to run only the tests for this node
    CMD=("${GOBIN}/ginkgo" "run")
    
    if [[ -n "${GINKGO_FOCUS}" ]]; then
        CMD+=("--focus" "${GINKGO_FOCUS}")
    fi
    
    if [[ -n "${GINKGO_LABEL_FILTER}" ]]; then
        CMD+=("--label-filter" "${GINKGO_LABEL_FILTER}")
    fi
    
    # Combine all tests for this node into a single focus pattern
    # Use regex OR (|) to match any of the tests
    if [[ -s "${NODE_TESTS}" ]]; then
        # Read all tests and join them with | for regex OR
        FOCUS_PATTERN=$(paste -sd '|' "${NODE_TESTS}")
        echo "Focus pattern for node ${GINKGO_NODE}: ${FOCUS_PATTERN}"
        CMD+=("--focus" "${FOCUS_PATTERN}")
    fi
    
    # Add the test directories
    CMD+=("${GO_E2E_DIRS[@]}")
    
    # Clean up temporary files
    rm -f "${TEMP_TEST_LIST}" "${NODE_TESTS}"
    
else
    # Original behavior - run all tests
    CMD=("${GOBIN}/ginkgo" "run")
    
    if [[ -n "${GINKGO_FOCUS}" ]]; then
        CMD+=("--focus" "${GINKGO_FOCUS}")
    fi
    
    if [[ -n "${GINKGO_LABEL_FILTER}" ]]; then
        CMD+=("--label-filter" "${GINKGO_LABEL_FILTER}")
    fi
    
    CMD+=("${GO_E2E_DIRS[@]}")
fi

CMD+=(--timeout 120m --race -vv --procs "${GINKGO_PROCS}" --show-node-events --trace --force-newlines --output-interceptor-mode "${GINKGO_OUTPUT_INTERCEPTOR_MODE}" --github-output --output-dir "${REPORTS}" --junit-report junit_e2e_test.xml --keep-separate-reports)

echo "Running e2e tests with ${GINKGO_PROCS} parallel processes..."
echo "Output interceptor mode: ${GINKGO_OUTPUT_INTERCEPTOR_MODE} (dup=show all output, swap=clean output)"
echo "Reports will be saved to: ${REPORTS}"
echo "Individual worker reports will be preserved with --keep-separate-reports"

# Run the command
"${CMD[@]}"
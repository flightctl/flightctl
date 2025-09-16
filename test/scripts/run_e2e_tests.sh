#!/usr/bin/env bash
set -euo pipefail

# This script now calls the Go-based test runner
# The original shell script has been moved to run_e2e_tests_shell.sh.bak

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

# Set up environment variables like the original script did
export API_ENDPOINT=https://$(get_endpoint_host flightctl-api-route)
export REGISTRY_ENDPOINT=$(registry_address)

# Pass all arguments to the Go script
go run "${SCRIPT_DIR}/run_e2e_tests.go" "$@"
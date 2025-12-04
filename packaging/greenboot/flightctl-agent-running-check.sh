#!/bin/bash
#
# Greenboot health check for flightctl-agent.
# Installed to: /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
#
set -x -euo pipefail

# shellcheck source=packaging/greenboot/functions.sh
source /usr/share/flightctl/functions/greenboot.sh

# Exit handler for consistent logging (preserve original exit code)
trap 'rc=$?; [ "$rc" -ne 0 ] && log_error "FAILURE" || log_info "FINISHED"; exit $rc' EXIT

# Exit if not root
if [ "$(id -u)" -ne 0 ]; then
    echo "The '${SCRIPT_NAME}' script must be run with root privileges"
    exit 1
fi

echo "STARTED"
print_boot_status

# Get dynamic timeout based on boot counter
WAIT_TIMEOUT=$(get_wait_timeout)

# Wait for service to be active and stable
if ! wait_for "$WAIT_TIMEOUT" "$DEFAULT_SUCCESS_DURATION" verify_flightctl_agent_status; then
    log_error "flightctl-agent.service did not stabilize within ${WAIT_TIMEOUT}s"
    exit 1
fi

log_info "flightctl-agent.service is healthy"

#!/bin/bash
#
# Greenboot health check for flightctl-agent.
# Installed to: /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
#
set -x -euo pipefail

# shellcheck source=packaging/greenboot/functions.sh
source /usr/share/flightctl/functions/greenboot.sh

# Exit handler for consistent logging (preserve original exit code)
trap 'rc=$?; [ "$rc" -ne 0 ] && log_error "[health-check] FAILED (exit code: $rc)" || log_info "[health-check] FINISHED successfully"; exit $rc' EXIT

# Exit if not root
if [ "$(id -u)" -ne 0 ]; then
    echo "The '${SCRIPT_NAME}' script must be run with root privileges"
    exit 1
fi

log_info "=== flightctl-agent greenboot health check started ==="
log_info "Timeout: ${FLIGHTCTL_HEALTH_CHECK_TIMEOUT}s (Phase 1: wait for active, Phase 2: 60s stability window)"
print_boot_status

# Run health check via Go binary
# Checks: service status + reports agent's self-reported connectivity (informational)
if ! flightctl-agent health --timeout="${FLIGHTCTL_HEALTH_CHECK_TIMEOUT}s" --verbose; then
    log_error "flightctl-agent health check failed"
    exit 1
fi

log_info "flightctl-agent is healthy"

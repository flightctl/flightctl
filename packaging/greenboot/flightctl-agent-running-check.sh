#!/bin/bash
#
# Greenboot health check for flightctl-agent.
# Installed to: /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
#
set -x -euo pipefail

# shellcheck source=packaging/greenboot/functions.sh
source /usr/share/flightctl/functions/greenboot.sh

# The three values below are passed straight through as flags to
# `flightctl-agent health` below, which uses them to decide whether the
# current boot passes or fails:
#
#   Phase 1 (--timeout): wait this long for flightctl-agent.service to become
#   active. If it never does, this script exits non-zero and greenboot treats
#   the boot as failed (eventually triggering a rollback reboot).
#
#   Phase 2 (--stability-window): once active, keep re-checking for this long
#   to catch a crash-loop before declaring the boot stable.
#
#   --poll-interval: how often, during both phases above, to re-check
#   flightctl-agent.service's status.

# Phase 1 timeout, in seconds. Should be >= systemd's TimeoutStartSec (default ~90s).
FLIGHTCTL_HEALTH_CHECK_TIMEOUT=150

# Phase 2 stability window, in seconds.
FLIGHTCTL_HEALTH_STABILITY_WINDOW=60

# Poll interval used during both phases, in seconds.
FLIGHTCTL_HEALTH_POLL_INTERVAL=5

# Allow the three values above to be tuned via greenboot.conf, same as
# GREENBOOT_MAX_BOOT_ATTEMPTS and other greenboot settings.
if [ -f "$GREENBOOT_CONF" ]; then
    # shellcheck disable=SC1090
    source "$GREENBOOT_CONF"
fi

# Exit handler for consistent logging (preserve original exit code)
trap 'rc=$?; [ "$rc" -ne 0 ] && log_error "[health-check] FAILED (exit code: $rc)" || log_info "[health-check] FINISHED successfully"; exit $rc' EXIT

# Exit if not root
if [ "$(id -u)" -ne 0 ]; then
    echo "The '${SCRIPT_NAME}' script must be run with root privileges"
    exit 1
fi

log_info "=== flightctl-agent greenboot health check started ==="
log_info "Phase 1 timeout: ${FLIGHTCTL_HEALTH_CHECK_TIMEOUT}s (wait for active), Phase 2: ${FLIGHTCTL_HEALTH_STABILITY_WINDOW}s stability window, poll interval: ${FLIGHTCTL_HEALTH_POLL_INTERVAL}s"
print_boot_status

# Run health check via Go binary
# Checks: service status + reports agent's self-reported connectivity (informational)
if ! flightctl-agent health \
    --timeout="${FLIGHTCTL_HEALTH_CHECK_TIMEOUT}s" \
    --stability-window="${FLIGHTCTL_HEALTH_STABILITY_WINDOW}s" \
    --poll-interval="${FLIGHTCTL_HEALTH_POLL_INTERVAL}s" \
    --verbose; then
    log_error "flightctl-agent health check failed"
    exit 1
fi

log_info "flightctl-agent is healthy"

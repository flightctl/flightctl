#!/bin/bash
#
# Pre-rollback script for flightctl-agent.
# Runs AFTER health checks fail and BEFORE system rolls back.
# Installed to: /usr/lib/greenboot/red.d/40_flightctl_agent_pre_rollback.sh
#
# Exit codes:
#   0 - Success (does not affect rollback)
#   non-zero - Failure (logged but does not prevent rollback)
#
# Note: This script's exit code does NOT affect the rollback process.
# Greenboot will proceed with rollback regardless of this script's outcome.
#
set -x -e -o pipefail

# shellcheck source=packaging/greenboot/functions.sh
source /usr/share/flightctl/functions/greenboot.sh

# Exit handler (preserve original exit code)
trap 'rc=$?; [ "$rc" -ne 0 ] && echo "[pre-rollback] FAILED (exit code: $rc)" || echo "[pre-rollback] FINISHED successfully"; exit $rc' EXIT

# Exit if not root
if [ "$(id -u)" -ne 0 ]; then
    echo "The '${SCRIPT_NAME}' script must be run with root privileges"
    exit 1
fi

log_info "=== flightctl-agent pre-rollback script started ==="
print_boot_status

# Only collect debug info if rollback is imminent
if ! grub2-editenv - list 2>/dev/null | grep -q '^boot_counter=0'; then
    log_info "System is not scheduled to rollback"
    exit 0
fi

log_info "Greenboot health check failed rollback imminent - collecting debug info"
collect_debug_info

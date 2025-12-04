#!/bin/bash
#
# Pre-rollback script for flightctl-agent.
# Runs AFTER health checks fail and BEFORE system rolls back.
# Installed to: /usr/lib/greenboot/red.d/40_flightctl_agent_pre_rollback.sh
#
set -x -e -o pipefail

# shellcheck source=packaging/greenboot/functions.sh
source /usr/share/flightctl/functions/greenboot.sh

# Exit handler (preserve original exit code)
trap 'rc=$?; [ "$rc" -ne 0 ] && echo "FAILURE" || echo "FINISHED"; exit $rc' EXIT

# Exit if not root
if [ "$(id -u)" -ne 0 ]; then
    echo "The '${SCRIPT_NAME}' script must be run with root privileges"
    exit 1
fi

echo "STARTED"
print_boot_status

# Only collect debug info if rollback is imminent
if ! grub2-editenv - list 2>/dev/null | grep -q '^boot_counter=0'; then
    log_info "System is not scheduled to roll back"
    exit 0
fi

log_info "Rollback imminent - collecting debug info"
collect_debug_info

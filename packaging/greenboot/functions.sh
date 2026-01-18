#!/bin/bash
#
# Shared functions for flightctl greenboot health check scripts.
# Installed to: /usr/share/flightctl/functions/greenboot.sh
#

SCRIPT_NAME=$(basename "$0")

#
# Constants
#

# Static timeout for health check polling (in seconds).
# This should be >= systemd's TimeoutStartSec (default ~90s).
FLIGHTCTL_HEALTH_CHECK_TIMEOUT=150

#
# Logging
#

log_info() {
    echo "[${SCRIPT_NAME}] INFO: $*"
}

log_error() {
    echo "[${SCRIPT_NAME}] ERROR: $*" >&2
}

#
# Boot status
#

# Print GRUB boot variables and OS status for debugging
print_boot_status() {
    log_info "GRUB boot variables:"
    grub2-editenv - list 2>/dev/null | grep ^boot_ || echo "None"

    if command -v ostree &>/dev/null; then
        log_info "ostree status:"
        ostree admin status 2>/dev/null || echo "N/A"
    fi

    if command -v bootc &>/dev/null; then
        log_info "bootc status:"
        bootc status --booted 2>/dev/null || echo "N/A"
    fi
}

#
# Debug info collection (used by pre-rollback script)
#

collect_debug_info() {
    log_info "Service status:"
    systemctl status flightctl-agent.service --no-pager 2>&1 || true

    log_info "Recent journal entries:"
    journalctl -u flightctl-agent.service -n 50 --no-pager 2>&1 || true
}

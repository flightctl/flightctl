#!/bin/bash
#
# Shared functions for flightctl greenboot health check scripts.
# Installed to: /usr/share/flightctl/functions/greenboot.sh
#

SCRIPT_NAME=$(basename "$0")
SCRIPT_PID=$$

# Default configuration
DEFAULT_BASE_TIMEOUT=150
DEFAULT_MAX_BOOT_ATTEMPTS=3

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
# Dynamic timeout
#

# Get wait timeout that increases with each boot attempt.
# Formula: base_timeout * (max_boots - boot_counter)
#   Boot 1 (counter=2): 150 * 1 = 150s
#   Boot 2 (counter=1): 150 * 2 = 300s
#   Boot 3 (counter=0): 150 * 3 = 450s
get_wait_timeout() {
    local conf=/etc/greenboot/greenboot.conf
    # shellcheck source=/dev/null
    [ -f "$conf" ] && source "$conf"

    local base=${FLIGHTCTL_WAIT_TIMEOUT_SEC:-${DEFAULT_BASE_TIMEOUT}}
    local max_boots=${GREENBOOT_MAX_BOOT_ATTEMPTS:-${DEFAULT_MAX_BOOT_ATTEMPTS}}

    local counter
    counter=$(grub2-editenv - list 2>/dev/null | grep ^boot_counter= | cut -d= -f2 || echo "")
    [ -z "$counter" ] && counter=$((max_boots - 1))

    local mult=$((max_boots - counter))
    [ "$mult" -le 0 ] && mult=1

    echo "[${SCRIPT_NAME}] INFO: Boot counter: ${counter}, Multiplier: ${mult}, Timeout: $((base * mult))s" >&2
    echo $((base * mult))
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

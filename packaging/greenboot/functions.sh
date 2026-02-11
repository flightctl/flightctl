#!/usr/bin/env bash  
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

# Greenboot configuration file path
GREENBOOT_CONF="/etc/greenboot/greenboot.conf"

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

#
# Greenboot configuration (used by flightctl-configure-greenboot.service)
#

# Find third-party application health check scripts in required.d directories
# Preserves core greenboot scripts and flightctl scripts
find_third_party_scripts() {
    local scripts=""
    for dir in /usr/lib/greenboot/check/required.d /etc/greenboot/check/required.d; do
        [ -d "$dir" ] || continue
        for script in "$dir"/*.sh; do
            [ -f "$script" ] || continue
            local name
            name=$(basename "$script")
            # Skip flightctl's own health check scripts
            case "$name" in
                *flightctl*) continue ;;
            esac
            # Skip core greenboot scripts (from greenboot package)
            case "$name" in
                00_required_scripts_start.sh) continue ;;
                01_repository_dns_check.sh) continue ;;
                02_watchdog.sh) continue ;;
            esac
            scripts="$scripts \"$name\""
        done
    done
    echo "$scripts"
}

# Set DISABLED_HEALTHCHECKS in greenboot.conf
set_disabled_healthchecks() {
    local disabled_scripts="$1"

    mkdir -p "$(dirname "$GREENBOOT_CONF")"
    touch "$GREENBOOT_CONF"

    # Remove existing DISABLED_HEALTHCHECKS line and add new one
    local tmp_conf="$GREENBOOT_CONF.tmp.$$"
    grep -v '^DISABLED_HEALTHCHECKS=' "$GREENBOOT_CONF" > "$tmp_conf" 2>/dev/null || true
    echo "DISABLED_HEALTHCHECKS=($disabled_scripts)" >> "$tmp_conf"
    mv "$tmp_conf" "$GREENBOOT_CONF"
}

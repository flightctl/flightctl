#!/usr/bin/env bash  
#
# Shared functions for flightctl greenboot health check scripts.
# Installed to: /usr/share/flightctl/functions/greenboot.sh
#

SCRIPT_NAME=$(basename "$0")

#
# Constants
#

# Greenboot configuration file path (override in tests via GREENBOOT_CONF)
GREENBOOT_CONF="${GREENBOOT_CONF:-/etc/greenboot/greenboot.conf}"

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

# Bundled vendor health checks to disable wherever they appear.
# MicroShift installs under /etc; some images place checks under /usr/lib.
# Non-blocklisted customer scripts in either path participate in rollback by default.
DISABLED_VENDOR_HEALTHCHECKS=(
    40_microshift_running_check.sh
)

# Return true if name is already listed in the disabled scripts string.
_is_already_listed() {
    local scripts="$1"
    local name="$2"
    case " $scripts " in
        *" \"$name\" "*) return 0 ;;
        *) return 1 ;;
    esac
}

# Find blocklisted vendor health checks in /usr/lib and /etc required.d.
# Only names in DISABLED_VENDOR_HEALTHCHECKS are returned; customer scripts are left alone.
find_blocked_vendor_healthchecks() {
    local usr_lib_dir="${GREENBOOT_USR_LIB_REQUIRED_D:-/usr/lib/greenboot/check/required.d}"
    local etc_dir="${GREENBOOT_ETC_REQUIRED_D:-/etc/greenboot/check/required.d}"
    local scripts=""
    local dir script name blocked

    for dir in "$usr_lib_dir" "$etc_dir"; do
        [ -d "$dir" ] || continue
        for script in "$dir"/*.sh; do
            [ -f "$script" ] || continue
            name=$(basename "$script")
            for blocked in "${DISABLED_VENDOR_HEALTHCHECKS[@]}"; do
                if [ "$name" = "$blocked" ]; then
                    if ! _is_already_listed "$scripts" "$name"; then
                        scripts="$scripts \"$name\""
                    fi
                    break
                fi
            done
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

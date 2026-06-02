#!/bin/bash
#
# Mask bootc-fetch-apply-updates.timer so flightctl manages OS updates.
# Installed to: /usr/libexec/flightctl/mask-bootc-timer.sh
#
# RPM %post may create the mask during image build, but on bootc/composefs images
# that symlink often does not persist on the running system. Invoked from
# flightctl-agent.service ExecStartPre (and flightctl-mask-bootc-timer.service on
# boot) so /etc gets the mask before the bootc-timer e2e readlink check.
#
set -euo pipefail

# Debug logging
LOGFILE="/var/log/flightctl-mask-bootc-timer.log"
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "${LOGFILE}"
}

log "=== mask-bootc-timer.sh starting ==="

readonly TIMER_NAME="bootc-fetch-apply-updates.timer"
readonly UNIT_FILE="/usr/lib/systemd/system/${TIMER_NAME}"
readonly MASK_LINK="/etc/systemd/system/${TIMER_NAME}"

# Detection matches bootc-timer e2e (agent_bootc_timer_test.go) and flightctl.spec %post.
bootc_timer_present() {
    log "Checking if bootc timer is present..."

    if [ -f "${UNIT_FILE}" ]; then
        log "  Found via unit file: ${UNIT_FILE}"
        return 0
    fi
    log "  Unit file not found at ${UNIT_FILE}"

    if find /usr/lib/systemd -name "${TIMER_NAME}" -quit 2>/dev/null | grep -q .; then
        log "  Found via find in /usr/lib/systemd"
        return 0
    fi
    log "  Not found via find in /usr/lib/systemd"

    if systemctl list-unit-files "${TIMER_NAME}" 2>/dev/null | grep -q "^${TIMER_NAME}"; then
        log "  Found via systemctl list-unit-files"
        return 0
    fi
    log "  Not found via systemctl list-unit-files"

    log "Bootc timer not detected by any method"
    return 1
}

mask_already_applied() {
    if [ -L "${MASK_LINK}" ] && [ "$(readlink "${MASK_LINK}")" = "/dev/null" ]; then
        log "Mask already applied: ${MASK_LINK} -> /dev/null"
        return 0
    fi
    log "Mask not yet applied (link does not exist or points elsewhere)"
    return 1
}

if ! bootc_timer_present; then
    log "Bootc timer not present, exiting"
    exit 0
fi

if mask_already_applied; then
    log "Mask already applied, exiting"
    exit 0
fi

log "Applying mask..."
mkdir -p /etc/systemd/system
ln -sf /dev/null "${MASK_LINK}"
log "Created symlink: ${MASK_LINK} -> /dev/null"

systemctl daemon-reload 2>/dev/null || true
log "Ran systemctl daemon-reload"

systemctl mask --now "${TIMER_NAME}" 2>/dev/null || true
log "Ran systemctl mask --now ${TIMER_NAME}"

log "=== mask-bootc-timer.sh completed successfully ==="

#!/bin/bash
#
# Disable known bundled vendor greenboot health checks (e.g. MicroShift) so they
# do not gate OS rollback. Customer scripts in /etc or /usr/lib are preserved.
# Runs before greenboot-healthcheck.service on every boot.
#
# Installed to: /usr/libexec/flightctl/configure-greenboot.sh
#
set -x -euo pipefail

source /usr/share/flightctl/functions/greenboot.sh

disabled_scripts=$(find_blocked_vendor_healthchecks)

if [ -z "$disabled_scripts" ]; then
    log_info "No bundled vendor greenboot health checks to disable"
else
    log_info "Disabling bundled vendor greenboot health checks:$disabled_scripts"
fi

set_disabled_healthchecks "$disabled_scripts"
log_info "Updated $GREENBOOT_CONF"

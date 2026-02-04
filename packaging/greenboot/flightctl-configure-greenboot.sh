#!/bin/bash
#
# Configure greenboot to only allow flightctl health checks to trigger rollback.
# Runs before greenboot-healthcheck.service on every boot.
#
# Installed to: /usr/libexec/flightctl/configure-greenboot.sh
#
set -x -euo pipefail

source /usr/share/flightctl/functions/greenboot.sh

disabled_scripts=$(find_third_party_scripts)

if [ -z "$disabled_scripts" ]; then
    log_info "No third-party greenboot health checks found"
    exit 0
fi

log_info "Disabling third-party greenboot health checks:$disabled_scripts"
set_disabled_healthchecks "$disabled_scripts"
log_info "Updated $GREENBOOT_CONF"

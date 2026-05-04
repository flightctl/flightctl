#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh

configure_memory_overcommit() {
    local current_value
    current_value=$(sysctl -n vm.overcommit_memory 2>/dev/null || echo "unknown")

    if [ "$current_value" != "1" ]; then
        echo "Configuring memory overcommit for Redis/Valkey (current: $current_value)"

        # Set the value immediately
        sysctl vm.overcommit_memory=1

        # Make it persistent by adding to /etc/sysctl.conf if not already present
        if ! grep -q "^vm.overcommit_memory\s*=" /etc/sysctl.conf 2>/dev/null; then
            echo "vm.overcommit_memory = 1" >> /etc/sysctl.conf
            echo "Added vm.overcommit_memory = 1 to /etc/sysctl.conf"
        else
            # Update existing entry if different
            sed -i 's/^vm.overcommit_memory\s*=.*/vm.overcommit_memory = 1/' /etc/sysctl.conf
            echo "Updated existing vm.overcommit_memory setting in /etc/sysctl.conf"
        fi

        echo "Memory overcommit configured successfully"
    else
        echo "Memory overcommit already configured (vm.overcommit_memory = 1)"
    fi
}

main() {
    echo "Configuring KV secrets"
    ensure_kv_secrets

    echo "Configuring system parameters for KV service"
    configure_memory_overcommit

    echo "KV configuration complete"
}

main

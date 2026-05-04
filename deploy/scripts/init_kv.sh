#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh

configure_memory_overcommit() {
    local current_value
    current_value=$(sysctl -n vm.overcommit_memory 2>/dev/null || echo "unknown")

    echo "Configuring memory overcommit for Redis/Valkey (current: $current_value)"

    # Set the value immediately if needed
    if [ "$current_value" != "1" ]; then
        sysctl -w vm.overcommit_memory=1
        echo "Runtime memory overcommit setting updated"
    else
        echo "Runtime memory overcommit already configured"
    fi

    # Always ensure persistence in /etc/sysctl.conf regardless of current runtime value
    if [ ! -f /etc/sysctl.conf ]; then
        if ! touch /etc/sysctl.conf 2>/dev/null; then
            echo "Error: Cannot create /etc/sysctl.conf - persistence may fail" >&2
            return 1
        fi
    fi

    if ! [ -w /etc/sysctl.conf ]; then
        echo "Error: Cannot write to /etc/sysctl.conf - persistence may fail" >&2
        return 1
    fi

    # Check if setting exists and update/add as needed
    if ! grep -q "^vm.overcommit_memory[[:space:]]*=" /etc/sysctl.conf 2>/dev/null; then
        if ! echo "vm.overcommit_memory = 1" >> /etc/sysctl.conf; then
            echo "Error: Failed to add vm.overcommit_memory setting to /etc/sysctl.conf" >&2
            return 1
        fi
        echo "Added vm.overcommit_memory = 1 to /etc/sysctl.conf"
    else
        # Update existing entry to ensure it's set to 1
        if ! sed -i 's/^vm.overcommit_memory[[:space:]]*=.*/vm.overcommit_memory = 1/' /etc/sysctl.conf; then
            echo "Error: Failed to update vm.overcommit_memory setting in /etc/sysctl.conf" >&2
            return 1
        fi
        echo "Updated existing vm.overcommit_memory setting in /etc/sysctl.conf"
    fi

    echo "Memory overcommit configured successfully"
}

main() {
    echo "Configuring KV secrets"
    ensure_kv_secrets

    echo "Configuring system parameters for KV service"
    configure_memory_overcommit

    echo "KV configuration complete"
}

main

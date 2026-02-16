#!/usr/bin/env bash
# Shared container configuration functions

# Get the directory where this script is located
get_config_dir() {
    echo "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
}

# Load flavor configuration from config file
# Usage: load_flavor_config <flavor>
# Sets: EL_FLAVOR, EL_VERSION, BUILD_IMAGE, RUNTIME_IMAGE, MINIMAL_IMAGE, PAM_BASE_URL, PAM_PACKAGE_VERSION
load_flavor_config() {
    local flavor="$1"
    local config_file="$(get_config_dir)/container-flavors.conf"

    if [[ ! -f "$config_file" ]]; then
        echo "Error: Configuration file not found: $config_file" >&2
        return 1
    fi

    # Read the config file and find the matching flavor
    local config_line
    config_line=$(grep "^${flavor}:" "$config_file" 2>/dev/null)

    if [[ -z "$config_line" ]]; then
        echo "Error: Flavor '$flavor' not found in configuration" >&2
        echo "Available flavors:" >&2
        get_available_flavors >&2
        return 1
    fi

    # Parse the colon-separated configuration
    IFS=':' read -r EL_FLAVOR EL_VERSION BUILD_IMAGE RUNTIME_IMAGE MINIMAL_IMAGE PAM_BASE_URL PAM_PACKAGE_VERSION <<< "$config_line"

    # Export variables for use in calling scripts
    export EL_FLAVOR EL_VERSION BUILD_IMAGE RUNTIME_IMAGE MINIMAL_IMAGE PAM_BASE_URL PAM_PACKAGE_VERSION

    echo "Loaded configuration for $EL_FLAVOR (EL$EL_VERSION)"
}

# Get list of available flavors from config file
get_available_flavors() {
    local config_file="$(get_config_dir)/container-flavors.conf"

    if [[ ! -f "$config_file" ]]; then
        echo "Error: Configuration file not found: $config_file" >&2
        return 1
    fi

    grep '^[a-z]' "$config_file" | grep -v '^#' | cut -d':' -f1 | sort
}

# Validate that a flavor exists
validate_flavor() {
    local flavor="$1"
    local available_flavors_space_separated

    available_flavors_space_separated=$(get_available_flavors | tr '\n' ' ')
    if [[ ! " $available_flavors_space_separated " =~ " $flavor " ]]; then
        echo "Error: Invalid flavor '$flavor'" >&2
        echo "Available flavors: $available_flavors_space_separated" >&2
        return 1
    fi

    return 0
}
#!/usr/bin/env bash
# Shared container configuration functions

# Get the directory where this script is located
get_config_dir() {
    echo "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
}

# Parse YAML value from helm chart config (without requiring yq)
parse_helm_config_value() {
    local config_file="$1"
    local flavor_section="$2"
    local key_path="$3"

    # Extract the flavor section and then find the key
    awk "
        /^${flavor_section}:/ { in_section=1; next }
        /^[a-zA-Z]/ && in_section { in_section=0 }
        in_section && /${key_path}:/ {
            gsub(/.*${key_path}:[[:space:]]*/, \"\")
            gsub(/[[:space:]]*#.*/, \"\")
            gsub(/\"/, \"\")
            print
            exit
        }
    " "$config_file"
}

# Load flavor configuration from helm chart config
# Usage: load_flavor_config <flavor>
# Environment: Set FORCE_COMMUNITY_IMAGES=false to use Red Hat registry images (requires authentication)
# Sets: EL_FLAVOR, EL_VERSION, BUILD_IMAGE, RUNTIME_IMAGE, MINIMAL_IMAGE, PACKAGE_MINIMAL_IMAGE, PAM_BASE_URL, PAM_PACKAGE_VERSION, RPM_MOCK_ROOT
load_flavor_config() {
    local flavor="$1"
    local config_file="$(get_config_dir)/../deploy/helm/helm-chart-opts.yaml"

    if [[ ! -f "$config_file" ]]; then
        echo "Error: Helm configuration file not found: $config_file" >&2
        return 1
    fi

    # Determine flavor section and version
    # Use FORCE_COMMUNITY_IMAGES=false to use Red Hat registry images (requires authentication)
    local flavor_section
    case "$flavor" in
        el9)
            if [[ "${FORCE_COMMUNITY_IMAGES:-true}" == "true" ]] || ! grep -q "^redhat-el9:" "$config_file"; then
                flavor_section="community-el9"
            else
                flavor_section="redhat-el9"
            fi
            EL_VERSION="9"
            ;;
        el10)
            if [[ "${FORCE_COMMUNITY_IMAGES:-true}" == "true" ]] || ! grep -q "^redhat-el10:" "$config_file"; then
                flavor_section="community-el10"
            else
                flavor_section="redhat-el10"
            fi
            EL_VERSION="10"
            ;;
        *)
            echo "Error: Flavor '$flavor' not found in configuration" >&2
            echo "Available flavors:" >&2
            get_available_flavors >&2
            return 1
            ;;
    esac

    # Parse configuration values from helm config
    BUILD_IMAGE=$(parse_helm_config_value "$config_file" "$flavor_section" "buildImage")
    RUNTIME_IMAGE=$(parse_helm_config_value "$config_file" "$flavor_section" "runtimeImage")
    PACKAGE_MINIMAL_IMAGE=$(parse_helm_config_value "$config_file" "$flavor_section" "packageMinimalImage")
    PAM_BASE_URL=$(parse_helm_config_value "$config_file" "$flavor_section" "pamBaseUrl")
    PAM_PACKAGE_VERSION=$(parse_helm_config_value "$config_file" "$flavor_section" "pamPackageVersion")
    RPM_MOCK_ROOT=$(parse_helm_config_value "$config_file" "$flavor_section" "rpmMockRoot")

    # Get minimal image (combines image and tag)
    local minimal_image_base=$(parse_helm_config_value "$config_file" "$flavor_section" "image")
    local minimal_image_tag=$(parse_helm_config_value "$config_file" "$flavor_section" "tag")
    MINIMAL_IMAGE="${minimal_image_base}:${minimal_image_tag}"

    # Set flavor name
    EL_FLAVOR="$flavor"

    # Validate that we got valid values
    if [[ -z "$BUILD_IMAGE" || -z "$RUNTIME_IMAGE" || -z "$MINIMAL_IMAGE" || -z "$PACKAGE_MINIMAL_IMAGE" ]]; then
        echo "Error: Failed to load complete configuration for flavor '$flavor'" >&2
        echo "BUILD_IMAGE=$BUILD_IMAGE"
        echo "RUNTIME_IMAGE=$RUNTIME_IMAGE"
        echo "MINIMAL_IMAGE=$MINIMAL_IMAGE"
        echo "PACKAGE_MINIMAL_IMAGE=$PACKAGE_MINIMAL_IMAGE"
        return 1
    fi

    # Export variables for use in calling scripts
    export EL_FLAVOR EL_VERSION BUILD_IMAGE RUNTIME_IMAGE MINIMAL_IMAGE PACKAGE_MINIMAL_IMAGE PAM_BASE_URL PAM_PACKAGE_VERSION RPM_MOCK_ROOT

    echo "Loaded configuration for $EL_FLAVOR (EL$EL_VERSION) using $flavor_section"
}

# Get list of available flavors from helm chart config
get_available_flavors() {
    local config_file="$(get_config_dir)/../deploy/helm/helm-chart-opts.yaml"

    if [[ ! -f "$config_file" ]]; then
        echo "Error: Helm configuration file not found: $config_file" >&2
        return 1
    fi

    # Extract flavor names from community-* and redhat-* sections and strip prefixes
    (grep '^community-' "$config_file" | cut -d':' -f1 | sed 's/community-//'; \
     grep '^redhat-' "$config_file" | cut -d':' -f1 | sed 's/redhat-//') | sort -u
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
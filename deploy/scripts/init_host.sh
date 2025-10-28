#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh
source "${SCRIPT_DIR}"/init_utils.sh

SERVICE_CONFIG_FILE="/etc/flightctl/service-config.yaml"

write_default_base_domain() {
    # Write base domain to the config file
    base_domain="$(ip route get 1.1.1.1 | grep -oP 'src \K\S+')"
    echo "Setting base domain to: ${base_domain}"
    sed -i "s/^\(\s*baseDomain\s*\):\s*.*$/\1: ${base_domain}/" "${SERVICE_CONFIG_FILE}"
}

# Configure PAM authentication for FlightCtl
configure_pam_auth() {
    echo "Configuring PAM authentication for FlightCtl..."
    
    PAM_CONFIG_FILE="/etc/pam.d/flightctl"
    
    # Check if already configured
    if [ -f "$PAM_CONFIG_FILE" ]; then
        echo "✓ PAM configuration already exists: $PAM_CONFIG_FILE"
        return 0
    fi
    
    # Create service-specific PAM config that includes system-auth
    # This follows the standard pattern used by other services (e.g., cups, vsftpd)
    echo "Creating FlightCtl PAM configuration..."
    cat > "$PAM_CONFIG_FILE" << 'EOF'
#%PAM-1.0
# FlightCtl PAM configuration
# Includes the standard RHEL authentication stack (system-auth)
# This automatically supports any system-configured authentication backend
auth       include      system-auth
account    include      system-auth
password   include      system-auth
session    include      system-auth
EOF

    chmod 644 "$PAM_CONFIG_FILE"
    chown root:root "$PAM_CONFIG_FILE"
    
    echo "✓ PAM authentication configured successfully"
    echo "  Config file: $PAM_CONFIG_FILE"
    echo "  Uses standard RHEL authentication stack (system-auth)"
}

main() {
    echo "Configuring Flight Control"

    ensure_secrets

    base_domain=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")
    if [[ -z "$base_domain" ]]; then
        write_default_base_domain
    else
        echo "Base domain already set to: $base_domain"
    fi
    
    # Configure PAM authentication when auth type is oidc and pamOidcIssuer is enabled
    auth_type=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE")
    pam_enabled=$(extract_value "global.auth.pamOidcIssuer.enabled" "$SERVICE_CONFIG_FILE")
    
    if [[ "$auth_type" == "oidc" ]] && [[ "$pam_enabled" == "true" ]]; then
        configure_pam_auth
    else
        echo "PAM OIDC not enabled (auth.type=$auth_type, pamOidcIssuer.enabled=$pam_enabled) - skipping PAM configuration"
    fi

    echo "Configuration complete"
}

main

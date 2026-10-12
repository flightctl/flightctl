#!/usr/bin/env bash

set -euo pipefail

# Load shared functions first to get the constant directory paths
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh
validate_runtime_output_paths

OS="${OS:-el9}"
export OS

SYSTEMCTL_SCOPE_ARGS=()
if [[ ${EUID} -ne 0 ]]; then
  SYSTEMCTL_SCOPE_ARGS=(--user)
  require_rootless_local_podman
  if ! systemctl --user show-environment >/dev/null 2>&1; then
    echo "Error: the systemd user manager is unavailable; log in through a systemd session or enable lingering for this user" >&2
    exit 1
  fi
  systemctl --user import-environment XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME
fi

echo "Starting Deployment"

# Host directory for TPM manufacturer / swtpm CA PEMs (mounted read-only into flightctl-api)
install -d -m 0755 "${CONFIG_WRITEABLE_DIR}/tpm-cas"
install -d -m 0755 "${CONFIG_WRITEABLE_DIR}/flightctl-worker/registries.conf.d"
install -d -m 0755 "${CONFIG_WRITEABLE_DIR}/flightctl-delta-worker/registries.conf.d"

# Render quadlet files
bin/flightctl-standalone render quadlets \
  --config "packaging/images/${OS}/local-images.yaml" \
  --readonly-config-dir "${CONFIG_READONLY_DIR}" \
  --writeable-config-dir "${CONFIG_WRITEABLE_DIR}" \
  --quadlet-dir "${QUADLET_FILES_OUTPUT_DIR}" \
  --systemd-dir "${SYSTEMD_UNIT_OUTPUT_DIR}" \
  --bin-dir "${BIN_OUTPUT_DIR}" \
  --var-tmp-dir "${VAR_TMP_OUTPUT_DIR}" \
  --var-lib-dir "${VAR_LIB_OUTPUT_DIR}"

source "${SCRIPT_DIR}"/secrets.sh
ensure_delta_generation_secrets

if [[ -f $SCRIPT_DIR/local-ca/ca.crt ]] && [[ -f $SCRIPT_DIR/local-ca/ca.key ]]; then
  cp "$SCRIPT_DIR"/local-ca/ca.* "${CONFIG_WRITEABLE_DIR}/pki/"
  if [[ ${EUID} -eq 0 ]]; then
    chown root:root "${CONFIG_WRITEABLE_DIR}"/pki/ca.*
  fi
fi

echo "Starting all Flight Control services via target..."
start_service "flightctl.target"

echo "Waiting for services to initialize..."

# Check if we're using external database
if is_external_database_enabled; then
    echo "External database configured - skipping local database readiness check"
else
    # Wait for database to be ready first
    timeout --foreground 120s bash -c '
        while true; do
            if podman ps --quiet --filter "name=flightctl-db" | grep -q . && \
               podman exec flightctl-db pg_isready -U postgres >/dev/null 2>&1; then
                echo "Database is ready"
                break
            fi
            echo "Waiting for database to become ready..."
            sleep 3
        done
    '
fi

# Wait for database migration to complete
echo "Waiting for database migration to complete..."
timeout --foreground 120s bash -c '
    systemctl_scope_args=("$@")
    while true; do
        if systemctl "${systemctl_scope_args[@]}" is-active --quiet flightctl-db-migrate.service; then
            echo "Database migration completed"
            break
        fi
        echo "Waiting for database migration to complete..."
        sleep 3
    done
    ' _ "${SYSTEMCTL_SCOPE_ARGS[@]}"

# Wait for key-value service
timeout --foreground 60s bash -c '
    while true; do
        if podman ps --quiet --filter "name=flightctl-kv" | grep -q .; then
            # Determine which CLI to use based on the OS
            if [[ "${OS}" == "el10" ]]; then
                if podman exec flightctl-kv valkey-cli ping >/dev/null 2>&1; then
                    echo "Key-value service (Valkey) is ready"
                    break
                fi
            else
                if podman exec flightctl-kv redis-cli ping >/dev/null 2>&1; then
                    echo "Key-value service (Redis) is ready"
                    break
                fi
            fi
        fi
        echo "Waiting for key-value service..."
        sleep 2
    done
'

echo "Waiting for all services to be fully ready..."
# Get all services from flightctl.target
ALL_SERVICES=$(run_systemctl show flightctl.target -p Wants --value | tr ' ' '\n' | grep -E '^flightctl-.*\.service$' | sort)

# Wait for core services to be ready
start_time=$(date +%s)
timeout_seconds=120

while true; do
    current_time=$(date +%s)
    elapsed=$((current_time - start_time))

    if [ $elapsed -ge $timeout_seconds ]; then
        echo "Timeout: Core services did not become ready within ${timeout_seconds} seconds"
        exit 1
    fi

    # Check if target is active
    if ! run_systemctl is-active --quiet flightctl.target; then
        echo "Waiting for flightctl.target to become active..."
        sleep 3
        continue
    fi

    # Check each service
    all_active=true
    for service in ${ALL_SERVICES}; do
        if ! run_systemctl is-active --quiet "$service"; then
            echo "Waiting for service $service to become active..."
            all_active=false
            break
        fi
    done

    if $all_active; then
        echo "All services are active and ready"
        break
    fi

    sleep 3
done

# Create default PAM user for local development
PAM_USER="${PAM_USER:-admin}"
PAM_PASSWORD="${PAM_PASSWORD:-flightctl}"

echo "Creating default PAM user '${PAM_USER}'..."
# Create the flightctl-admin group (ignore if exists)
podman exec flightctl-pam-issuer groupadd flightctl-admin 2>/dev/null || true
# Create the user (skip if already exists)
if podman exec flightctl-pam-issuer id "$PAM_USER" >/dev/null 2>&1; then
    echo "User '${PAM_USER}' already exists, skipping creation"
else
    podman exec flightctl-pam-issuer adduser "$PAM_USER"
    podman exec flightctl-pam-issuer sh -c "echo '${PAM_USER}:${PAM_PASSWORD}' | chpasswd"
    podman exec flightctl-pam-issuer usermod -aG flightctl-admin "$PAM_USER"
    echo "PAM user '${PAM_USER}' created with admin role"
fi

echo "Deployment completed successfully!"
echo ""
echo "Flight Control services are running:"
for service in ${ALL_SERVICES}; do
    # Extract a human-readable name from the service name
    service_name=$(echo "$service" | sed 's/flightctl-//g' | sed 's/\.service//g' | sed 's/-/ /g' | sed 's/\b\w/\u&/g')
    if run_systemctl is-active --quiet "$service"; then
        echo "  ✓ $service_name ($service)"
    else
        echo "  ✗ $service_name ($service) - not active"
    fi
done

echo ""
if [[ ${EUID} -eq 0 ]]; then
  echo "You can check status with: systemctl status flightctl.target"
else
  echo "You can check status with: systemctl --user status flightctl.target"
fi

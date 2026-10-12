#!/usr/bin/env bash
# Wrapper around the image's set_passwords.sh start script.
#
# Mounted at /opt/app-root/src/postgresql-start/, which the sclorg entrypoint
# gives precedence over its own start/ directory, so the original script stays
# in place at "original" and can still be delegated to.
#
# The entrypoint *sources* these files and then goes on to start the real
# server, so all work happens inside a function: "local -" keeps the shell
# options set here from leaking into the entrypoint, and returning instead of
# exiting lets startup continue.
#
# The original script can hang when pg_authid is locked. If it does, fall back
# to checking whether the roles it would have touched already have a password
# and continue when they do, so upgrades still work on a degraded catalog.

flightctl_set_passwords() {
    local -
    set -euo pipefail

    local original="/usr/share/container-scripts/postgresql/start/set_passwords.sh"
    local script_timeout=60
    local query_timeout=10
    local roles=()
    local role has_password

    # postinitdb_actions is a plain (non-exported) variable of the entrypoint
    # shell; the original script reads it to decide whether to set the
    # application user's password, so pass it to the subprocess explicitly.
    if postinitdb_actions="${postinitdb_actions:-}" \
        timeout "${script_timeout}" bash "${original}"; then
        return 0
    fi

    echo "WARNING: set_passwords.sh did not complete within ${script_timeout}s"

    # Only verify roles the original script would actually set a password for.
    if [ -n "${POSTGRESQL_MASTER_USER:-}" ]; then
        roles+=("${POSTGRESQL_MASTER_USER}")
    fi
    if [ -n "${POSTGRESQL_USER:-}" ]; then
        roles+=("${POSTGRESQL_USER}")
    fi
    # The image only sets the postgres admin password when
    # POSTGRESQL_ADMIN_PASSWORD is defined.
    if [ -n "${POSTGRESQL_ADMIN_PASSWORD:-}" ]; then
        roles+=("postgres")
    fi

    if [ ${#roles[@]} -eq 0 ]; then
        echo "No role passwords are configured, continuing startup"
        return 0
    fi

    for role in "${roles[@]}"; do
        # Bound the fallback query as well: the catalog may still be blocked.
        if ! has_password=$(timeout "${query_timeout}" psql -U postgres -tAc \
            "SELECT rolpassword IS NOT NULL FROM pg_authid WHERE rolname='${role}'" 2>/dev/null); then
            has_password=""
        fi
        if [ "${has_password}" != "t" ]; then
            echo "ERROR: ${role} has no password set - cannot continue"
            return 1
        fi
    done

    echo "All role passwords are already set, continuing startup"
    return 0
}

flightctl_set_passwords || return 1

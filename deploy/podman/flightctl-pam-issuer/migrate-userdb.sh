#!/bin/sh
# Migration from 1.0.0: Copy user database files from old /etc volume to new location.
# TODO: Remove this migration script once 1.0.0 becomes unsupported.

set -e

USERDB_DIR=/etc/flightctl/flightctl-pam-issuer/userdb
MIGRATION_MARKER="$USERDB_DIR/.migrated"

# Skip if migration already completed
if [ -f "$MIGRATION_MARKER" ]; then
    exit 0
fi

OLD_VOL=$(podman volume inspect flightctl-pam-issuer-etc --format "{{.Mountpoint}}" 2>/dev/null || true)

if [ -n "$OLD_VOL" ] && [ -f "$OLD_VOL/passwd" ]; then
    for f in passwd shadow group gshadow; do
        if [ -f "$OLD_VOL/$f" ] && { [ ! -f "$USERDB_DIR/$f" ] || [ ! -s "$USERDB_DIR/$f" ]; }; then
            cp -p "$OLD_VOL/$f" "$USERDB_DIR/$f"
            echo "Migrated $f from old volume"
        fi
    done
fi

# Mark migration as complete
touch "$MIGRATION_MARKER"

exit 0

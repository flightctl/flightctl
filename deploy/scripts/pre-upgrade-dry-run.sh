#!/usr/bin/env bash

set -euo pipefail

IMAGE_TAG="${1:-latest}"
DB_SETUP_IMAGE="quay.io/flightctl/flightctl-db-setup:${IMAGE_TAG}"
PODMAN="${PODMAN:-$(command -v podman || echo '/usr/bin/podman')}"
CONF_FILE="/etc/flightctl/flightctl-services-install.conf"

# shellcheck source=/etc/flightctl/flightctl-services-install.conf
if [ -f "${CONF_FILE}" ]; then
  . "${CONF_FILE}"
fi

RUN_DRY_RUN="${RUN_DRY_RUN:-${FLIGHTCTL_MIGRATION_DRY_RUN:-0}}"
DB_WAIT_TIMEOUT="${DB_WAIT_TIMEOUT:-${FLIGHTCTL_DB_WAIT_TIMEOUT:-60}}"
DB_WAIT_SLEEP="${DB_WAIT_SLEEP:-${FLIGHTCTL_DB_WAIT_SLEEP:-1}}"

echo "[flightctl] pre-upgrade migration dry-run (tag=${IMAGE_TAG})"

if [ "${RUN_DRY_RUN}" != "1" ]; then
  echo "[flightctl] dry-run disabled; skipping"
  exit 0
fi

if [ ! -x "${PODMAN}" ]; then
  echo "[flightctl] podman not found; skipping"
  exit 0
fi

if "${PODMAN}" container exists flightctl-db >/dev/null 2>&1; then
  echo "[flightctl] waiting for database (timeout=${DB_WAIT_TIMEOUT}s sleep=${DB_WAIT_SLEEP}s)"
  if ! "${PODMAN}" run --rm --network flightctl \
    -e DB_HOST=flightctl-db -e DB_PORT=5432 -e DB_NAME=flightctl \
    -e DB_USER=admin \
    --secret flightctl-postgresql-master-password,type=env,target=DB_PASSWORD \
    "${DB_SETUP_IMAGE}" /app/deploy/scripts/wait-for-database.sh --timeout="${DB_WAIT_TIMEOUT}" --sleep="${DB_WAIT_SLEEP}"; then
    wait_exit=$?
    echo "[flightctl] database wait failed (exit code: ${wait_exit}); skipping dry-run"
    exit 0
  fi

  echo "[flightctl] running database migration dry-run"
  if "${PODMAN}" run --rm --network flightctl \
    -e DB_MIGRATION_USER=flightctl_migrator \
    --secret flightctl-postgresql-migrator-password,type=env,target=DB_MIGRATION_PASSWORD \
    -v /etc/flightctl/flightctl-api/config.yaml:/root/.flightctl/config.yaml:ro,z \
    "${DB_SETUP_IMAGE}" /usr/local/bin/flightctl-db-migrate --dry-run; then
    echo "[flightctl] dry-run completed successfully"
  else
    echo "[flightctl] dry-run failed (exit code: $?)"
  fi
else
  echo "[flightctl] database container not found; skipping"
fi

exit 0

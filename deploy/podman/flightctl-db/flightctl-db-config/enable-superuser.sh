#!/usr/bin/env bash

set -e

_psql () { psql --set ON_ERROR_STOP=1 "$@" ; }

# Ensure POSTGRESQL_MASTER_USER is treated as a superuser
if [ -n "${POSTGRESQL_MASTER_USER}" ]; then
    echo "Granting superuser privileges to ${POSTGRESQL_MASTER_USER}"
    _psql -c "ALTER ROLE $(echo "SELECT quote_ident('${POSTGRESQL_MASTER_USER}');" | _psql -t -A) WITH SUPERUSER;"
fi

#!/usr/bin/env bash

set -euo pipefail

DB_NAMESPACE=flightctl-internal
PG_USER=admin
PG_DATABASE=flightctl
PG_HOST=127.0.0.1
PG_PORT=5432
export PGPASSWORD=adminpass

METHOD=${1:-"kubectl"}

if [ "$METHOD" == "podman" ];
then
    until podman exec flightctl-db pg_isready -U ${PG_USER} --dbname ${PG_DATABASE} --host ${PG_HOST} --port ${PG_PORT}; do sleep 1; done
else
    echo "Waiting for postgress deployment to be ready"
    kubectl rollout status deployment flightctl-db -n ${DB_NAMESPACE} -w --timeout=300s

    DB_POD=$(kubectl get pod -n ${DB_NAMESPACE} -L flightctl.service=flightctl-db --no-headers -o custom-columns=":metadata.name" --context kind-kind )
    set -x
    until kubectl exec --context kind-kind -n ${DB_NAMESPACE} ${DB_POD} -- psql -h ${PG_HOST} -U ${PG_USER} -d ${PG_DATABASE} -c "select 1" > /dev/null 2>&1;
    do
        sleep 1;
    done
fi

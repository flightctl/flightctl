#!/bin/bash
podman run --name test-exit -d alpine sh -c 'sleep 5 && exit 0'
podman events --since $(date --utc -u +%Y-%m-%dT%H:%M:%SZ) --stream=false
echo "--- manually stopped ---"
podman run --name test-stop -d alpine sh -c 'trap "exit 0" TERM; sleep 100'
sleep 2
podman stop test-stop
podman events --since $(date --utc -u -d "10 seconds ago" +%Y-%m-%dT%H:%M:%SZ) --stream=false

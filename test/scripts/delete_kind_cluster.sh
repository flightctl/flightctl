#!/usr/bin/env bash
set -euo pipefail

if [[ "$(id -u)" -eq 0 ]]; then
    exec kind delete cluster
fi

"$(dirname "$(readlink -f "$0")")/runtime_preflight.sh" clean
export KIND_EXPERIMENTAL_PROVIDER=podman
if command -v systemd-run >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    if systemd-run --scope --user -p Delegate=yes kind delete cluster; then
        exit 0
    fi
    echo "Warning: delegated user scope unavailable; retrying Kind cleanup directly." >&2
fi

# Deleting a cluster does not normally need cgroup delegation, so retain a
# direct cleanup path for hosts where the user systemd manager is unavailable.
exec kind delete cluster

#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

podman build -f test/scripts/Containerfile.gitserver -t localhost/git-server:latest .


# can be tested with: 
# podman run -d --restart always -p 1213:22 --name gitserver --cap-add sys_chroot localhost/git-server:latest
# podman rm gitserver --force

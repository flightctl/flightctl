#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)

mkdir -p bin/.ssh/

# if bin/.ssh/id_rsa exists we just exit
if [ ! -f bin/.ssh/id_rsa ]; then
  echo "bin/.ssh/id_rsa does not exist, creating ssh-keygen"
  ssh-keygen -t rsa -b 4096 -f bin/.ssh/id_rsa -N "" -C "e2e test key"
fi

podman build -f test/scripts/Containerfile.gitserver -t localhost/git-server:latest .


# can be tested with: 
# podman run -d --restart always -p 1213:22 --name gitserver --cap-add sys_chroot localhost/git-server:latest
# podman rm gitserver --force

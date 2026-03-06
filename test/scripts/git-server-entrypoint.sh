#!/bin/bash
# Entrypoint for the git server container.
# Key pair is baked into the image at build time; the test copies the private key from the container.

set -euo pipefail

exec /usr/sbin/sshd -D -e -p 2222 \
    -o PidFile=none \
    -o StrictModes=no \
    -o UsePAM=no \
    -o AuthorizedKeysFile=/home/user/.ssh/authorized_keys

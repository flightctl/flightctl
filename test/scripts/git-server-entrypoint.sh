#!/bin/bash
# Entrypoint script for the git server container.

set -euo pipefail

CURRENT_UID=$(id -u)

# If we're not running as the 'user' UID from /etc/passwd, update it
if ! grep -q "^user:.*:${CURRENT_UID}:" /etc/passwd 2>/dev/null; then
    # Create a modified passwd entry for 'user' with the current UID
    # This allows sshd to successfully authenticate and switch to this user
    sed "s/^user:x:[0-9]*:/user:x:${CURRENT_UID}:/" /etc/passwd > /tmp/passwd.new
    cat /tmp/passwd.new > /etc/passwd
    rm -f /tmp/passwd.new
fi

exec /usr/sbin/sshd -D -e -p 2222 \
    -o PidFile=none \
    -o StrictModes=no \
    -o UsePAM=no \
    -o AuthorizedKeysFile=/etc/ssh/authorized_keys/%u

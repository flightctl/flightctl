#!/usr/bin/env sh
set -eu

# OpenSSH requires a passwd entry even when the remote username is explicit.
# Keep identity files on the writable temporary volume for arbitrary Pod UIDs.
identity_dir=$(mktemp -d "${TMPDIR:-/tmp}/flightctl-identity-XXXXXX")
uid=$(id -u)
gid=$(id -g)
home_dir=${HOME:-/var/lib/flightctl}
cat /etc/passwd > "$identity_dir/passwd"
printf 'flightctl:x:%s:%s:Flight Control:%s:/sbin/nologin\n' "$uid" "$gid" "$home_dir" >> "$identity_dir/passwd"
export NSS_WRAPPER_PASSWD="$identity_dir/passwd"
export NSS_WRAPPER_GROUP=/etc/group
export LD_PRELOAD="/usr/lib64/libnss_wrapper.so${LD_PRELOAD:+:$LD_PRELOAD}"
exec "$@"

#!/bin/sh

set -e

if [ -d "/proc/sys/net/ipv4" ] && [ -d "/proc/sys/net/ipv6" ]; then
    CONFIG_FILE="/etc/nginx/nginx.conf"
elif [ -d "/proc/sys/net/ipv4" ]; then
    CONFIG_FILE="/etc/nginx/nginx.conf.ipv4"
elif [ -d "/proc/sys/net/ipv6" ]; then
    CONFIG_FILE="/etc/nginx/nginx.conf.ipv6"
else
    echo "Unable to identify IP configuration"
    exit 1
fi

sed "s|{{CLI_ARTIFACTS_BASE_URL}}|${CLI_ARTIFACTS_BASE_URL}|g" < /home/server/src/gh-archives/index.json > /tmp/index.json && \
cat /tmp/index.json > /home/server/src/gh-archives/index.json


exec nginx -c "${CONFIG_FILE}" -g "daemon off;"

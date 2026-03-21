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

if [ -n "${TLS_CERT}" ] && [ -n "${TLS_KEY}" ]; then
    cat > /tmp/nginx-ssl-snippet.conf <<EOF
        ssl_certificate ${TLS_CERT};
        ssl_certificate_key ${TLS_KEY};
EOF
    sed -e 's/listen       8090 default_server/listen       8090 ssl default_server/' \
        -e 's/listen       \[::\]:8090 default_server/listen       [::]:8090 ssl default_server/' \
        -e '/server_name  _;/r /tmp/nginx-ssl-snippet.conf' \
        "${CONFIG_FILE}" > /tmp/nginx.conf
    CONFIG_FILE="/tmp/nginx.conf"
fi

exec nginx -c "${CONFIG_FILE}" -g "daemon off;"

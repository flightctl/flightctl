#!/bin/bash
set -euo pipefail

if [ -z "${1:-}" ]; then
    echo "ERROR: Mirror registry URL required as first argument" >&2
    exit 1
fi

MIRROR="$1"

case "${MIRROR}" in
    *[!a-zA-Z0-9._:/-]*)
        echo "ERROR: Invalid mirror registry format: ${MIRROR}" >&2
        exit 1
        ;;
esac

mkdir -p /etc/containers/registries.conf.d

cat > /etc/containers/registries.conf.d/100-mirror-registry.conf <<EOF
[[registry]]
prefix = "quay.io:443"
location = "${MIRROR}"
insecure = true

[[registry]]
prefix = "quay.io"
location = "${MIRROR}"
insecure = true

[[registry]]
prefix = "registry.redhat.io"
location = "${MIRROR}"
insecure = true

[[registry]]
prefix = "registry.access.redhat.com"
location = "${MIRROR}"
insecure = true
EOF

cat > /etc/containers/policy.json <<EOF
{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {
    "docker": {
      "${MIRROR}": [{"type": "insecureAcceptAnything"}]
    },
    "docker-daemon": {
      "": [{"type": "insecureAcceptAnything"}]
    }
  }
}
EOF

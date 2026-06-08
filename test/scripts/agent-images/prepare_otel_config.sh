#!/usr/bin/env bash
# prepare_otel_config.sh
# Parses Flightctl agent config, writes OTEL CA/env, and blocks until certs exist.
# Intended for use as systemd ExecStartPre= for the otelcol service.

set -euo pipefail

log() { echo "[prepare-otel] $*" >&2; }
die() { echo "[prepare-otel][ERROR] $*" >&2; exit 1; }

# ----------------------------
# Configurable defaults (env)
# ----------------------------
DIR="${DIR:-/etc/otelcol}"
CFG="${CFG:-/etc/flightctl/config.yaml}"
OTEL_USER="${OTEL_USER:-otelcol}"
OTEL_GROUP="${OTEL_GROUP:-$OTEL_USER}"
OTEL_CERT_WAIT_SECS="${OTEL_CERT_WAIT_SECS:-90}"     # how long to wait for cert/key
UMASK_VAL="${UMASK_VAL:-027}"                         # group-readable, world-no access

umask "$UMASK_VAL"

# ----------------------------
# Pre-flight
# ----------------------------
[ -f "$CFG" ] || die "Agent config not found at $CFG"
command -v yq >/dev/null 2>&1 || die "yq not found on PATH"

# base64 decode wrapper (Linux/macOS)
b64dec() {
  if base64 -d >/dev/null 2>&1 <<<""; then
    base64 -d
  else
    base64 -D
  fi
}

# ----------------------------
# 1) Derive gateway + decode CA
# ----------------------------
server_url="$(yq -e -r '.["enrollment-service"].service.server // ""' "$CFG" 2>/dev/null || true)"
[ -n "${server_url:-}" ] || die "Missing enrollment-service.service.server in $CFG"

# Extract IP and determine DNS suffix (nip.io for IPv4, sslip.io for IPv6)
dns_suffix=""
ip=""

# Try sslip.io pattern (IPv6: fd2e-6f44-fddb--6a.sslip.io or api.fd2e-6f44-fddb--6a.sslip.io)
if [[ "$server_url" == *".sslip.io"* ]]; then
  # Extract segment immediately before .sslip.io (handles optional service prefixes)
  host="$(printf '%s\n' "$server_url" | sed -n 's#.*://\(.*\)\.sslip\.io.*#\1#p')"
  if [[ -n "$host" ]]; then
    ip="${host##*.}"
    dns_suffix="sslip.io"
  fi
fi

# Try nip.io pattern (IPv4: 192.168.122.75.nip.io or api.192.168.122.75.nip.io)
if [ -z "$ip" ] && [[ "$server_url" == *".nip.io"* ]]; then
  # Match full IPv4 address (4 octets) before .nip.io to avoid matching service prefixes
  ip="$(printf '%s\n' "$server_url" | sed -n 's#.*\([0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\)\.nip\.io.*#\1#p')"
  dns_suffix="nip.io"
fi

# Fallback: try to extract IPv4 from URL
if [ -z "$ip" ]; then
  ip="$(printf '%s\n' "$server_url" | grep -Eo '([0-9]{1,3}\.){3}[0-9]{1,3}' || true)"
  dns_suffix="nip.io"
fi

[ -n "$ip" ] || die "Could not extract IP from server URL: $server_url"

# Read CA (base64)
ca_b64="$(yq -e -r '.["enrollment-service"].service."certificate-authority-data" // ""' "$CFG" 2>/dev/null || true)"
[ -n "${ca_b64:-}" ] || die "Missing certificate-authority-data in $CFG"

# Ensure dirs exist with proper ownership so otelcol can traverse/read
install -d -m 0755 "$DIR"
install -d -o root -g "$OTEL_GROUP" -m 0750 "$DIR/certs"

# Atomic write of CA file
tmp_ca="$(mktemp "${DIR}/certs/.gateway-ca.XXXXXX")"
printf '%s' "$ca_b64" | b64dec > "$tmp_ca" || { rm -f "$tmp_ca"; die "Failed to decode certificate-authority-data (base64)"; }
chown root:"$OTEL_GROUP" "$tmp_ca"
chmod 0640 "$tmp_ca"
mv -f "$tmp_ca" "$DIR/certs/gateway-ca.crt"
log "Wrote CA to $DIR/certs/gateway-ca.crt"

# Build gateway endpoint (gRPC/4317) using the same DNS suffix as server URL
gateway="telemetry-gateway.${ip}.${dns_suffix}:4317"
log "Derived OTEL_GATEWAY=${gateway}"

# Build OTEL options; allow optional server_name_override via env OTLP_SERVER_NAME
opts="--config=${DIR}/config.yaml"
if [ -n "${OTLP_SERVER_NAME:-}" ]; then
  opts="$opts --set exporters.otlp.tls.server_name_override=${OTLP_SERVER_NAME}"
  log "Using server_name_override=${OTLP_SERVER_NAME}"
fi

# Atomic write env file used by systemd EnvironmentFile=
tmp_env="$(mktemp "${DIR}/.otelcol.conf.XXXXXX")"
cat > "$tmp_env" <<EOF
OTEL_GATEWAY=${gateway}
OTELCOL_OPTIONS=${opts}
EOF
chown root:"$OTEL_GROUP" "$tmp_env"
chmod 0640 "$tmp_env"
mv -f "$tmp_env" "${DIR}/otelcol.conf"
log "Wrote ${DIR}/otelcol.conf"

# ----------------------------
# 2) Wait for provisioned client certs
# ----------------------------
CRTF="${DIR}/certs/otel.crt"
KEYF="${DIR}/certs/otel.key"

log "Waiting up to ${OTEL_CERT_WAIT_SECS}s for ${CRTF} and ${KEYF} ..."
end=$(( $(date +%s) + OTEL_CERT_WAIT_SECS ))

while [ "$(date +%s)" -lt "$end" ]; do
  if [ -s "$CRTF" ] && [ -s "$KEYF" ]; then
    chown root:"$OTEL_GROUP" "$CRTF" "$KEYF" || true
    chmod 0640 "$CRTF" || true
    chmod 0640 "$KEYF" || true
    # Apply SELinux labels if available (Fedora/RHEL)
    if command -v restorecon >/dev/null 2>&1; then
      restorecon -R "$DIR" || true
    fi
    log "Found certs and fixed ownership/permissions."
    exit 0
  fi
  sleep 1
done

die "Timed out waiting for ${CRTF} / ${KEYF}"

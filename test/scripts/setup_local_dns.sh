#!/usr/bin/env bash
# setup_local_dns.sh — Configure local DNS so CI never depends on external nip.io.
# Uses standalone CoreDNS on the host + patched in-cluster CoreDNS.
# Both resolve nip.io locally and forward everything else to upstream.
# No dnsmasq. No cross-connections between host and cluster DNS.
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/functions"

IP="${1:-$(get_ext_ip)}"
[[ "${IP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "ERROR: '${IP}' is not a valid IPv4 address" >&2; exit 1; }
echo "=== Setting up local DNS for ${IP}.nip.io ==="

# 1. Patch in-cluster CoreDNS ConfigMap with nip.io template zone
kubectl patch configmap coredns -n kube-system --type=merge \
  --patch-file "${SCRIPT_DIR}/../configs/coredns-local-dns-patch.yaml"

# 2. Restart in-cluster CoreDNS so it loads the new config
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

# 3. Download the standalone CoreDNS binary for the host
COREDNS_VERSION="1.14.7"
COREDNS_DIR="/tmp/coredns-host"
mkdir -p "${COREDNS_DIR}"
curl -fsSL "https://github.com/coredns/coredns/releases/download/v${COREDNS_VERSION}/coredns_${COREDNS_VERSION}_linux_amd64.tgz" \
  | tar -xz -C "${COREDNS_DIR}"
chmod +x "${COREDNS_DIR}/coredns"

# 4. Write host Corefile — same nip.io template as the cluster, catch-all forwards upstream
cat > "${COREDNS_DIR}/Corefile" <<'COREFILE_EOF'
.:53 {
    errors
    forward . /run/systemd/resolve/resolv.conf
    cache 30
    loop
    reload
}
nip.io:53 {
    errors
    template IN A nip.io {
        match ^(.*\.)?([0-9]+)\.([0-9]+)\.([0-9]+)\.([0-9]+)\.nip\.io\.$
        answer "{{ .Name }} 60 IN A {{ index .Group \"2\" }}.{{ index .Group \"3\" }}.{{ index .Group \"4\" }}.{{ index .Group \"5\" }}"
        fallthrough
    }
    template IN AAAA nip.io {
        rcode NOERROR
    }
}
COREFILE_EOF

# 5. Disable systemd-resolved stub listener so host CoreDNS can bind port 53
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo systemctl restart systemd-resolved

# 6. Point /etc/resolv.conf at the host CoreDNS (127.0.0.1)
sudo rm -f /etc/resolv.conf
echo "nameserver 127.0.0.1" | sudo tee /etc/resolv.conf

# 7. Start host CoreDNS in background
cd "${COREDNS_DIR}"
sudo ./coredns -conf Corefile &
COREDNS_PID=$!
echo "Host CoreDNS started (PID ${COREDNS_PID})"
sleep 2
# Verify it's still running
if ! sudo kill -0 "${COREDNS_PID}" 2>/dev/null; then
    echo "ERROR: Host CoreDNS exited unexpectedly" >&2
    exit 1
fi

# 8. Update the kind node's /etc/resolv.conf to point directly to upstream DNS.
#    Docker configured it at container creation time to use systemd-resolved's stub
#    listener, which we just disabled. Extract real upstream nameservers from
#    /run/systemd/resolve/resolv.conf and inject them into the kind node.
echo "Upstream nameservers for kind node:"
grep '^nameserver' /run/systemd/resolve/resolv.conf | head -3
grep '^nameserver' /run/systemd/resolve/resolv.conf | head -3 \
  | docker exec -i kind-control-plane sh -c "cat > /etc/resolv.conf"

# 9. Restart in-cluster CoreDNS again so it picks up the kind node's new upstream resolver
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

# 10. Spot-check: verify nip.io resolves via the host CoreDNS
getent hosts "api.${IP}.nip.io" || { echo "ERROR: api.${IP}.nip.io did not resolve" >&2; exit 1; }

echo "=== Local DNS setup complete ==="

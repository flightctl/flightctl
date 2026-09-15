#!/usr/bin/env bash
# setup_local_dns.sh — Configure local DNS so CI never depends on external nip.io.
# Uses CoreDNS hostPort + systemd-resolved forwarding. No /etc/hosts.
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/functions"

IP="${1:-$(get_ext_ip)}"
[[ "${IP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "ERROR: '${IP}' is not a valid IPv4 address" >&2; exit 1; }
echo "=== Setting up local DNS for ${IP}.nip.io ==="

# 1. Patch CoreDNS ConfigMap with nip.io template zone
kubectl patch configmap coredns -n kube-system --type=merge \
  --patch-file "${SCRIPT_DIR}/../configs/coredns-local-dns-patch.yaml"

# 2. Expose CoreDNS on the host network via hostPort
kubectl patch deployment coredns -n kube-system --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/ports/0/hostPort","value":53}]'

# 3. Restart CoreDNS and wait for it
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

# 4. Discover the kind node Docker IP
NODE_IP=$(docker inspect kind-control-plane -f '{{.NetworkSettings.Networks.kind.IPAddress}}')

# 5. Configure systemd-resolved to forward .nip.io to CoreDNS
sudo mkdir -p /etc/systemd/resolved.conf.d/
printf '[Resolve]\nDNS=%s\nDomains=~nip.io\n' "${NODE_IP}" | sudo tee /etc/systemd/resolved.conf.d/nip-io.conf
sudo systemctl restart systemd-resolved

# 6. Spot-check
getent hosts "api.${IP}.nip.io" || { echo "ERROR: api.${IP}.nip.io did not resolve" >&2; exit 1; }
echo "=== Local DNS setup complete ==="

#!/usr/bin/env bash
# setup_local_dns.sh — Configure local DNS so CI never depends on external nip.io.
# Uses CoreDNS NodePort + dnsmasq forwarding. No /etc/hosts. No systemd-resolved.
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/functions"

IP="${1:-$(get_ext_ip)}"
[[ "${IP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "ERROR: '${IP}' is not a valid IPv4 address" >&2; exit 1; }
echo "=== Setting up local DNS for ${IP}.nip.io ==="

# 1. Patch CoreDNS ConfigMap with nip.io template zone
kubectl patch configmap coredns -n kube-system --type=merge \
  --patch-file "${SCRIPT_DIR}/../configs/coredns-local-dns-patch.yaml"

# 2. Create a NodePort service exposing kube-dns externally (UDP)
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: kube-dns-nodeport
  namespace: kube-system
spec:
  type: NodePort
  selector:
    k8s-app: kube-dns
  ports:
    - protocol: UDP
      port: 53
      targetPort: 53
EOF

# 3. Restart CoreDNS and wait for it
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

# 4. Discover the kind node Docker IP
NODE_IP=$(docker inspect kind-control-plane -f '{{.NetworkSettings.Networks.kind.IPAddress}}')
echo "Kind node IP: ${NODE_IP}"

# 5. Get the assigned NodePort
NODE_PORT=$(kubectl get svc kube-dns-nodeport -n kube-system -o jsonpath='{.spec.ports[0].nodePort}')
echo "CoreDNS NodePort: ${NODE_PORT}"

# 6. Disable systemd-resolved stub listener (conflicts with dnsmasq on port 53)
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo systemctl restart systemd-resolved
# Point /etc/resolv.conf to dnsmasq (127.0.0.1) instead of stub-resolv.conf
sudo rm -f /etc/resolv.conf
echo "nameserver 127.0.0.1" | sudo tee /etc/resolv.conf

# 7. Install dnsmasq and configure it to forward .nip.io queries
sudo apt-get update -qq && sudo apt-get install -y -qq dnsmasq
cat <<DNSMASQ_EOF | sudo tee /etc/dnsmasq.d/nip-io.conf
server=/nip.io/${NODE_IP}#${NODE_PORT}
resolv-file=/run/systemd/resolve/resolv.conf
DNSMASQ_EOF
sudo systemctl restart dnsmasq

# 8. Spot-check
getent hosts "api.${IP}.nip.io" || { echo "ERROR: api.${IP}.nip.io did not resolve" >&2; exit 1; }
echo "=== Local DNS setup complete ==="

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

# 4. Discover the kind node Docker IP and the assigned NodePort
NODE_IP=$(docker inspect kind-control-plane -f '{{.NetworkSettings.Networks.kind.IPAddress}}')
echo "Kind node IP: ${NODE_IP}"
NODE_PORT=$(kubectl get svc kube-dns-nodeport -n kube-system -o jsonpath='{.spec.ports[0].nodePort}')
echo "CoreDNS NodePort: ${NODE_PORT}"

# 5. Install dnsmasq while systemd-resolved is still running (apt-get needs DNS)
sudo apt-get update -qq && sudo apt-get install -y -qq dnsmasq

# 6. Write dnsmasq config: forward .nip.io to CoreDNS, use resolved upstream for everything else
cat <<DNSMASQ_EOF | sudo tee /etc/dnsmasq.d/nip-io.conf
server=/nip.io/${NODE_IP}#${NODE_PORT}
resolv-file=/run/systemd/resolve/resolv.conf
DNSMASQ_EOF

# 7. Stop dnsmasq — it auto-started but cannot bind port 53 (systemd-resolved owns it)
sudo systemctl stop dnsmasq

# 8. Disable systemd-resolved stub listener so dnsmasq can have port 53
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo systemctl restart systemd-resolved

# 9. Point /etc/resolv.conf at dnsmasq (127.0.0.1)
sudo rm -f /etc/resolv.conf
echo "nameserver 127.0.0.1" | sudo tee /etc/resolv.conf

# 10. Start dnsmasq — port 53 is now free
sudo systemctl start dnsmasq

# 11. Spot-check
getent hosts "api.${IP}.nip.io" || { echo "ERROR: api.${IP}.nip.io did not resolve" >&2; exit 1; }

# 12. Fix kind node DNS — Docker configured it at creation time to forward to
#     systemd-resolved's stub listener, which we just disabled.  Point it at
#     dnsmasq on the host instead (reachable via the Docker gateway IP).
GATEWAY_IP=$(docker inspect kind-control-plane -f '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}')
echo "Docker gateway (dnsmasq from inside kind node): ${GATEWAY_IP}"
docker exec kind-control-plane sh -c "echo 'nameserver ${GATEWAY_IP}' > /etc/resolv.conf"

# 13. Restart CoreDNS so it picks up the updated /etc/resolv.conf upstream
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

echo "=== Local DNS setup complete ==="

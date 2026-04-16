#!/usr/bin/env bash
set -x -euo pipefail

# Check if helm is properly installed and executable
if command -v helm &>/dev/null && helm version --short &>/dev/null; then
    echo "Helm already installed: $(helm version --short)"
    exit 0
fi

# Remove any broken helm installations
sudo rm -f /usr/local/bin/helm 2>/dev/null || true

# Get the remote shell script and make sure it's the one we expect, inside the script there is also
# verification of the downloaded binaries
curl -fsSL -o /tmp/get_helm.sh https://raw.githubusercontent.com/helm/helm/0d0f91d1ce277b2c8766cdc4c7aa04dbafbf2503/scripts/get-helm-3
echo "6701e269a95eec0a5f67067f504f43ad94e9b4a52ec1205d26b3973d6f5cb3dc  /tmp/get_helm.sh" | sha256sum --check || exit 1
chmod a+x /tmp/get_helm.sh
/tmp/get_helm.sh

rm /tmp/get_helm.sh

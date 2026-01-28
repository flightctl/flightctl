#!/usr/bin/env bash
KIND_VERSION=0.26.0

EXISTING_VERSION=$(kind version 2>&1 | awk '{print $2}' | sed 's/v//')

if [ "$EXISTING_VERSION" == "$KIND_VERSION" ]; then
     echo "Kind v${KIND_VERSION} already installed"
else
    echo "Installing kind v${KIND_VERSION}"
    # Install kind
    go install sigs.k8s.io/kind@v${KIND_VERSION}
    sudo cp $(go env GOPATH)/bin/kind /usr/local/bin
fi

if which systemctl; then

    if [ -f /etc/systemd/system/user@.service.d/delegate.conf ]; then
        echo "Kind systemd rootless already configured" && exit 0
    else
        echo "Configuring Kind for rootless operation in Linux"
        # Enable rootless Kind, see https://kind.sigs.k8s.io/docs/user/rootless/
        sudo mkdir -p /etc/systemd/system/user@.service.d
        cat << EOF | sudo tee /etc/systemd/system/user@.service.d/delegate.conf > /dev/null
[Service]
Delegate=yes
EOF

        sudo systemctl daemon-reload
    fi
fi


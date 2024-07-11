#!/usr/bin/env bash

KUBECTL_VERSION="1.30.0"

which kubectl >/dev/null 2>&1
if [ $? -eq 0 ]; then
    echo "Kubectl already installed"
    exit 0
fi

echo "Installing kubect; v${KUBECTL_VERSION}"

# Install kubectl
arch=$(uname -m)
if [ $arch == "x86_64" ]; then 
    kube_arch="amd64"
    sha256="7c3807c0f5c1b30110a2ff1e55da1d112a6d0096201f1beb81b269f582b5d1c5"
elif [ $arch == "aarch64" ]; then
    kube_arch="arm64"
    sha256="669af0cf520757298ea60a8b6eb6b719ba443a9c7d35f36d3fb2fd7513e8c7d2"
else
    echo "Error: no kubectl available for $arch"
fi

echo ${sha256} > sha256

curl -LO "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${kube_arch}/kubectl"
echo "$(cat sha256) kubectl" | sha256sum --check 
if [ $? -eq 1 ]; then
	echo "kubectl failed checksum test"
	rm sha256
	exit 1
fi

rm sha256
sudo chmod +x kubectl
sudo chown root:root kubectl
sudo mv kubectl /usr/local/bin

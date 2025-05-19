#!/usr/bin/env bash
set -eo pipefail

GATEWAY_VERSION=1.0.0

echo "Installing Gateway API: v$GATEWAY_VERSION"
kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/v$GATEWAY_VERSION/standard-install.yaml"

echo "Installing Contour Gateway"
kubectl apply -f https://projectcontour.io/quickstart/contour-gateway-provisioner.yaml

kubectl apply -f test/scripts/gateway/gateway-class.yaml

kubectl apply -f test/scripts/gateway/contour-deployment.yaml

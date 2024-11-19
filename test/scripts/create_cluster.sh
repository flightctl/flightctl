#!/usr/bin/env bash

kind create cluster --config test/scripts/kind_cluster.yaml

if [ "$GATEWAY" ]; then
    test/scripts/gateway/install-gateway.sh
fi

echo ""

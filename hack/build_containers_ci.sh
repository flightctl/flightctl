#!/usr/bin/env bash
set -euo pipefail

# Dynamic container build script for CI
# Automatically determines the best build method for any flavor
# Usage: build_containers_ci.sh <flavor>

FLAVOR="${1:-el9}"

echo "Building containers for flavor: $FLAVOR"

# Try build methods in order of preference:
# 1. Flavor-specific make target (if exists)
# 2. Generic make build-containers-ci (if supports the flavor)
# 3. hack/publish_containers.sh build (if exists and supports the flavor)
# 4. Legacy make build-containers (fallback)

# Check if there's a flavor-specific make target
if make -n "build-containers-ci-${FLAVOR}" >/dev/null 2>&1; then
    echo "Using flavor-specific make target: build-containers-ci-${FLAVOR}"
    exec make -j4 "build-containers-ci-${FLAVOR}"
fi

# Check if build-containers-ci supports this flavor
if make -n build-containers-ci >/dev/null 2>&1; then
    # Check if it's explicitly for el9 only by looking at the Makefile
    if grep -q "build-containers-ci:.*el9" Makefile 2>/dev/null && [ "$FLAVOR" != "el9" ]; then
        echo "build-containers-ci target is el9-specific, skipping for $FLAVOR"
    else
        echo "Using make build-containers-ci for $FLAVOR"
        exec make -j4 build-containers-ci FLAVOR="$FLAVOR"
    fi
fi

# Check if hack/publish_containers.sh exists and supports build command
if [ -f "hack/publish_containers.sh" ] && grep -q "build)" "hack/publish_containers.sh" 2>/dev/null; then
    echo "Using hack/publish_containers.sh build for $FLAVOR"
    exec hack/publish_containers.sh build "$FLAVOR"
fi

# Fallback to legacy build-containers (builds all flavors)
if make -n build-containers >/dev/null 2>&1; then
    echo "Using fallback: make build-containers (builds all flavors)"
    exec make build-containers FLAVOR="$FLAVOR"
fi

echo "Error: No suitable build method found for flavor $FLAVOR"
echo "Available make targets:"
make -qp 2>/dev/null | grep -E '^[a-zA-Z0-9_-]+:' | cut -d: -f1 | grep -E 'container|build' | sort
exit 1
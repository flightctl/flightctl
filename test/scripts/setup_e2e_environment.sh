#!/bin/bash

# Setup script for e2e test environment
# This script ensures the base disk is in the correct location for libvirt access

set -e

echo "🔧 Setting up e2e test environment..."

# Get the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Create user-level libvirt images directory
LIBVIRT_IMAGES_DIR="$HOME/.local/share/libvirt/images"
mkdir -p "$LIBVIRT_IMAGES_DIR"

# Check if base disk exists in project directory
PROJECT_BASE_DISK="$PROJECT_ROOT/bin/output/qcow2/disk.qcow2"
LIBVIRT_BASE_DISK="$LIBVIRT_IMAGES_DIR/base-disk.qcow2"

if [ ! -f "$PROJECT_BASE_DISK" ]; then
    echo "❌ Base disk not found at $PROJECT_BASE_DISK"
    echo "Please build the project first: make build"
    exit 1
fi

# Check if we need to copy the base disk
if [ ! -f "$LIBVIRT_BASE_DISK" ] || [ "$PROJECT_BASE_DISK" -nt "$LIBVIRT_BASE_DISK" ]; then
    echo "📋 Copying base disk to libvirt images directory..."
    cp "$PROJECT_BASE_DISK" "$LIBVIRT_BASE_DISK"
    echo "✅ Base disk copied to $LIBVIRT_BASE_DISK"
else
    echo "✅ Base disk already up to date in libvirt images directory"
fi

# Verify libvirt can access the directory
echo "🔍 Verifying libvirt access..."
if [ -r "$LIBVIRT_BASE_DISK" ]; then
    echo "✅ Base disk is readable"
else
    echo "❌ Base disk is not readable"
    exit 1
fi

echo "🎉 e2e test environment setup complete!"
echo "Base disk location: $LIBVIRT_BASE_DISK" 
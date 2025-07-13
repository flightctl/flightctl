#!/bin/bash

# Enable BuildKit for Flight Control project
export DOCKER_BUILDKIT=1
export BUILDKIT_PROGRESS=plain

echo "✅ BuildKit enabled for Flight Control project"
echo "   DOCKER_BUILDKIT=$DOCKER_BUILDKIT"
echo "   BUILDKIT_PROGRESS=$BUILDKIT_PROGRESS"
echo ""
echo "💡 To make this permanent, add to your ~/.bashrc:"
echo "   source $(pwd)/scripts/enable-buildkit.sh" 
#!/usr/bin/env bash

set -e

echo "Initializing flightctl-ui configuration..."

# Source and destination paths are now mount points in the container
SOURCE_PATH="/source"
DEST_PATH="/destination"

# Check if source certificate exists
if [ ! -f "$SOURCE_PATH/ca.crt" ]; then
  echo "Error: ca.crt not found in source volume at $SOURCE_PATH."
  exit 1
fi

# Copy the certificate file
echo "Copying ca.crt to destination volume..."
cp "$SOURCE_PATH/ca.crt" "$DEST_PATH/ca.crt"

# Set appropriate permissions
# echo "Setting permissions on destination certificate..."
# chmod 644 "$DEST_PATH/ca.crt"

echo "Certificate transfer complete. ca.crt is now available in destination volume."

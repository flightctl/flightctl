#!/usr/bin/env bash

set -eo pipefail

## Shared utilities for usage by init scripts

# Function to extract a value from the YAML file
extract_value() {
    local key="$1"
    local file="$2"
    sed -n -E "s/^[[:space:]]*${key}:[[:space:]]*[\"']?([^\"'#]+)[\"']?.*$/\1/p" "$file"
}

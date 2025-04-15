#!/usr/bin/env bash

set -eo pipefail

## Shared utilities for usage by init scripts

# Function to extract a value from the YAML file
extract_value() {
    local key="$1"
    local file="$2"
    # Extract the value then trim leading and trailing whitespace
    sed -n -E "s/^[[:space:]]*${key}:[[:space:]]*[\"']?([^\"'#]+)[\"']?.*$/\1/p" "$file" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

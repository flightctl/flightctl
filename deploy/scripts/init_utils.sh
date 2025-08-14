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

# Wait for files with backoff
wait_for_files() {
    local files=("$@")
    local max_attempts=5
    local attempt=1
    local wait_time=2

    while [ $attempt -le $max_attempts ]; do
        local all_files_exist=true
        for file in "${files[@]}"; do
            if [ ! -f "$file" ]; then
                all_files_exist=false
                break
            fi
        done

        if [ "$all_files_exist" = true ]; then
            echo "All files found: ${files[*]}"
            return 0
        fi

        echo "Attempt $attempt/$max_attempts: Files not ready, waiting ${wait_time}s..."
        sleep $wait_time
        attempt=$((attempt + 1))
        wait_time=$((wait_time * 2))
    done

    echo "Error: Not all files found after $max_attempts attempts"
    echo "Missing files:"
    for file in "${files[@]}"; do
        if [ ! -f "$file" ]; then
            echo "  - $file"
        fi
    done
    return 1
}

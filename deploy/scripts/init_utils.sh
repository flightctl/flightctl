#!/usr/bin/env bash

set -eo pipefail

## Shared utilities for usage by init scripts

# Function to extract a value from the YAML file
# Supports nested paths like "global.auth.type"
extract_value() {
    local path="$1"
    local file="$2"
    
    # Split path by dots
    IFS='.' read -ra keys <<< "$path"
    
    # Use awk to navigate YAML hierarchy
    awk -v path="$path" '
    BEGIN {
        split(path, keys, "\\.")
        depth = 0
        target_depth = length(keys)
        found_depth = 0
    }
    
    # Skip empty lines and comments
    /^[[:space:]]*$/ { next }
    /^[[:space:]]*#/ { next }
    
    {
        # Calculate indentation level (number of leading spaces / 2)
        indent = (match($0, /[^[:space:]]/) - 1) / 2
        
        # Extract key and value
        line = $0
        sub(/^[[:space:]]*/, "", line)  # Remove leading whitespace
        
        # Check if this line has a key
        if (match(line, /^([^:]+):[[:space:]]*(.*)$/, arr)) {
            key = arr[1]
            value = arr[2]
            
            # Remove quotes and comments from value
            gsub(/^["\047]|["\047][[:space:]]*#.*$|["\047]$/, "", value)
            gsub(/#.*$/, "", value)
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
            
            # Check if this key matches our current depth in the path
            if (found_depth < target_depth && key == keys[found_depth + 1]) {
                # Check if indentation matches expected depth
                if (indent == found_depth) {
                    found_depth++
                    
                    # If we reached the target depth and have a value, print it
                    if (found_depth == target_depth && value != "") {
                        print value
                        exit 0
                    }
                }
            } else if (indent <= found_depth) {
                # We went back up the hierarchy, reset if needed
                if (indent < found_depth) {
                    found_depth = indent
                }
            }
        }
    }
    ' "$file" | head -1
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

#!/bin/bash

# Get the repository path from the environment variable
repo_dir="$COPR_BUILD_SOURCE"

# Extract information from the repository (e.g., version, commit hash)
version=$(git -C "$repo_dir" describe --tags --always)

# Update a configuration file or environment variable
echo "Version: $version" > version.txt


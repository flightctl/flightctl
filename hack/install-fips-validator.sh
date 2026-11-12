#!/usr/bin/env bash
set -euo pipefail

# Build the pinned upstream validator revision with the repository's Go toolchain.
validator_version=v0.0.0-20261112194341-8ad0f0fbb0d1
output=${1:?Usage: install-fips-validator.sh OUTPUT_PATH}
output=$(realpath -m "$output")
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT

# Resolve the repository's selected compiler before leaving its module.
GOROOT=$(go env GOROOT)
export GOROOT
export PATH="$GOROOT/bin:$PATH"
export GOTOOLCHAIN=local
cd "$build_dir"
export GOWORK=off
export GOFIPS140=off
export GOFLAGS=''
go mod download "github.com/flightctl/fips-validator@${validator_version}"
module_dir="$(go env GOMODCACHE)/github.com/flightctl/fips-validator@${validator_version}"
cp -R "$module_dir/." .
chmod -R u+w .
go test ./...
mkdir -p "$(dirname "$output")"
go build -o "$output" .

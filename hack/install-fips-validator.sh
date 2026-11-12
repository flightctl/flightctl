#!/usr/bin/env bash
set -euo pipefail

# Keep the compatibility patch pinned until the upstream validator supports
# native Go FIPS modules. OpenSSL checks remain enabled for legacy binaries.
validator_version=v0.0.0-20250930084220-ceca2caa6e48
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
output=${1:?Usage: install-fips-validator.sh OUTPUT_PATH}
output=$(realpath -m "$output")
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT

# Resolve the repository's selected compiler before leaving its module.
export GOROOT="$(go env GOROOT)"
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
git apply "$script_dir/fips-validator-native.patch"
go test ./...
mkdir -p "$(dirname "$output")"
go build -o "$output" .

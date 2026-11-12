#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
recipe=packaging/images/el9/Containerfile.imagebuilder-worker
mkdir -p "$test_dir/baseline/$(dirname "$recipe")"

# The repaired recipe must match the reviewed PR recipe, with the baseline SDK.
sed '1c FROM registry.access.redhat.com/ubi9/go-toolset:1.26.10-1793621864 as build' \
    "$script_dir/../$recipe" > "$test_dir/expected"
cp "$test_dir/expected" "$test_dir/baseline/$recipe"
git -C "$test_dir/baseline" apply --reverse "$script_dir/upgrade-baseline-ubi9.patch"
printf 'baseline module\n' > "$test_dir/baseline/go.mod"
printf 'baseline application\n' > "$test_dir/baseline/main.go"

# Exercise the real repair outside the checkout, including idempotent reruns.
cd /
bash "$script_dir/prepare-upgrade-baseline.sh" "$test_dir/baseline"
cmp "$test_dir/expected" "$test_dir/baseline/$recipe"
bash "$script_dir/prepare-upgrade-baseline.sh" "$test_dir/baseline"
cmp "$test_dir/expected" "$test_dir/baseline/$recipe"
[[ "$(cat "$test_dir/baseline/go.mod")" == 'baseline module' ]]
[[ "$(cat "$test_dir/baseline/main.go")" == 'baseline application' ]]

# Unexpected baseline recipes must fail without changing any files.
git -C "$test_dir/baseline" apply --reverse "$script_dir/upgrade-baseline-ubi9.patch"
sed -i 's/--nodocs podman/--nodocs unexpected-package podman/' "$test_dir/baseline/$recipe"
cp "$test_dir/baseline/$recipe" "$test_dir/unexpected"
if bash "$script_dir/prepare-upgrade-baseline.sh" "$test_dir/baseline" > "$test_dir/rejected.log" 2>&1; then
    echo 'Unexpected baseline recipe was silently accepted' >&2
    exit 1
fi
cmp "$test_dir/unexpected" "$test_dir/baseline/$recipe"
echo 'Upgrade baseline dependency repair tests passed'

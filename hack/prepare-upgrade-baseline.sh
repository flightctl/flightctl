#!/usr/bin/env bash
set -euo pipefail

# Repair only the known UBI9 package-install failure in the target branch's
# image recipe. Keep the baseline application and compiler at their original ref.
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
baseline=${1:?Usage: prepare-upgrade-baseline.sh BASELINE_CHECKOUT}
repair_patch="$script_dir/upgrade-baseline-ubi9.patch"

if git -C "$baseline" apply --reverse --check "$repair_patch" >/dev/null 2>&1; then
    echo 'Baseline already contains the UBI9 Netavark package repair'
    exit 0
fi

# A changed recipe needs review; never silently skip a repair that did not apply.
git -C "$baseline" apply --check "$repair_patch"
git -C "$baseline" apply "$repair_patch"
echo 'Applied baseline dependency repair: pin epoch-2 UBI9 Netavark with SHA-256 verification'

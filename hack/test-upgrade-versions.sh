#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
tag=v1.5.0-main-49-gf99870eaf
cat > "$test_dir/flightctl" <<'CLI'
#!/usr/bin/env bash
printf 'Client Version: %s\nServer Version: %s\n' "$ACTUAL_CLIENT" "$ACTUAL_SERVER"
exit "$CLI_STATUS"
CLI
chmod +x "$test_dir/flightctl"

check_versions() {
    local state=$1 client=$2 server=$3 expected_status=$4 cli_status=${5:-0} result=0
    EXPECTED_CLIENT_TAG="$tag" EXPECTED_SERVER_TAG="$tag" EXPECTED_SERVER_TREE_STATE="$state" \
        ACTUAL_CLIENT="$client" ACTUAL_SERVER="$server" CLI_STATUS="$cli_status" \
        bash "$script_dir/validate-upgrade-versions.sh" "$test_dir/flightctl" \
        > "$test_dir/output.log" 2>&1 || result=$?
    if [[ "$result" != "$expected_status" ]]; then
        cat "$test_dir/output.log" >&2
        echo "Unexpected validation status $result for $state, $client, $server" >&2
        exit 1
    fi
}

check_versions clean "$tag" "$tag" 0
check_versions dirty "$tag" "$tag-dirty" 0
check_versions clean "$tag" "$tag-dirty" 1
check_versions dirty "$tag" "$tag" 1
check_versions dirty "$tag" 'v1.5.0-main-48-g111111111-dirty' 1
check_versions clean 'v1.5.0-main-48-g111111111' "$tag" 1
check_versions unknown "$tag" "$tag" 1
check_versions clean "$tag" "$tag" 42 42
echo 'Upgrade version metadata regression tests passed'

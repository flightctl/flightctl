#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
mkdir -p "$test_dir/bin"
cat > "$test_dir/bin/go" <<'GO'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FLIGHTCTL_TEST_GO_CALLS"
exit 42
GO
chmod +x "$test_dir/bin/go"

# Compiler discovery must stop the installer with the original failure status.
result=0
FLIGHTCTL_TEST_GO_CALLS="$test_dir/go-calls" PATH="$test_dir/bin:$PATH" \
  bash "$script_dir/install-fips-validator.sh" "$test_dir/validator" \
  > "$test_dir/output.log" 2>&1 || result=$?
[[ "$result" == 42 ]]
[[ "$(cat "$test_dir/go-calls")" == 'env GOROOT' ]]
[[ ! -e "$test_dir/validator" ]]
echo 'FIPS validator compiler discovery failure test passed'

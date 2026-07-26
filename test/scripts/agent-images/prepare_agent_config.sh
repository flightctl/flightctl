#!/usr/bin/env bash
set -e -x -o pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/../functions

# Dup stderr to fd 3 so run_on_quadlet trace lines (test/scripts/functions) are printed, including
# when stderr is redirected (e.g. 2>/dev/null on a command substitution). Same as e2e_startup.sh.
exec 3>&2

mkdir -p bin/agent/etc/flightctl/certs

echo Requesting enrollment enrollment certificate/key and config for agent =====

ensure_organization_set

# remove any previous CSR with the same name in case it existed
./bin/flightctl delete csr/client-enrollment || true

./bin/flightctl certificate request -n client-enrollment  -d bin/agent/etc/flightctl/certs/ | tee bin/agent/etc/flightctl/config.yaml

status_update_interval=0m2s
spec_fetch_interval=0m2s
enrollment_verify_interval=0m2s
# Wider Cap/Steps so a short Interval doesn't exhaust the enrollment backoff (and
# trigger Restart=always) during VM-pool bootstrap before anything has approved yet.
enrollment_verify_cap=0m90s
enrollment_verify_steps=11
# Use external getopt for long options
options=$(getopt -o h --long status-update-interval:,spec-fetch-interval:,enrollment-verify-interval:,enrollment-verify-cap:,enrollment-verify-steps:,help -n "$0" -- "$@")
eval set -- "$options"
while true; do
  case "$1" in
  -h|--help) echo "Usage: $0 --status-update-interval=0m2s"; exit 1 ;;
  --status-update-interval) status_update_interval=$2; shift 2 ;;
  --spec-fetch-interval) spec_fetch_interval=$2; shift 2 ;;
  --enrollment-verify-interval) enrollment_verify_interval=$2; shift 2 ;;
  --enrollment-verify-cap) enrollment_verify_cap=$2; shift 2 ;;
  --enrollment-verify-steps) enrollment_verify_steps=$2; shift 2 ;;
  --) shift; break ;;
  *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

# - Enforce the agent to fetch the spec and update status every 2 seconds to improve the E2E test speed
# - Enrollment-verify-* control the agent's poll-for-approval backoff (production defaults:
#   interval 10s / cap 1m / steps 6). Interval is shortened for e2e speed; Cap/Steps are
#   widened so the short interval can't exhaust the backoff during pristine VM-pool
#   bootstrap before enrollment approval.
# - Include the custom system info collectors that were defined in the container image
cat <<EOF | tee -a  bin/agent/etc/flightctl/config.yaml
spec-fetch-interval: $spec_fetch_interval
status-update-interval: $status_update_interval
enrollment-verify-interval: $enrollment_verify_interval
enrollment-verify-cap: $enrollment_verify_cap
enrollment-verify-steps: $enrollment_verify_steps
system-info-custom:
  - siteName
  - emptyValue
EOF
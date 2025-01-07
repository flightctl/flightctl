#!/usr/bin/env bash
set -e -x -o pipefail

mkdir -p bin/agent/etc/flightctl/certs

echo Requesting enrollment enrollment certificate/key and config for agent =====
# remove any previous CSR with the same name in case it existed
./bin/flightctl delete csr/client-enrollment || true

./bin/flightctl certificate request -n client-enrollment  -d bin/agent/etc/flightctl/certs/ | tee bin/agent/etc/flightctl/config.yaml

status_update_interval=0m2s
spec_fetch_interval=0m2s
# Use external getopt for long options
options=$(getopt -o h --long status-update-interval:,spec-fetch-interval:,help -n "$0" -- "$@")
eval set -- "$options"
while true; do
  case "$1" in
  -h|--help) echo "Usage: $0 --status-update-interval=0m2s"; exit 1 ;;
  --status-update-interval) status_update_interval=$2; shift 2 ;;
  --spec-fetch-interval) spec_fetch_interval=$2; shift 2 ;;
  --) shift; break ;;
  *) echo "Invalid option: $1" >&2; exit 1 ;;
  esac
done

# enforce the agent to fetch the spec and update status every 2 seconds to improve the E2E test speed
cat <<EOF | tee -a  bin/agent/etc/flightctl/config.yaml
spec-fetch-interval: $spec_fetch_interval
status-update-interval: $status_update_interval
EOF

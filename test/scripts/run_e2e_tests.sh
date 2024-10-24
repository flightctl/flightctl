#!/usr/bin/env bash
set -x -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

REPORTS=${1}
GO_E2E_DIRS=${@:2}

go install github.com/onsi/ginkgo/v2/ginkgo

GOBIN=$(go env GOBIN)

export API_ENDPOINT=https://$(get_endpoint_host flightctl-api-route)
"${GOBIN}/ginkgo" run --timeout 30m --race -vv --junit-report ${REPORTS}/junit_e2e_test.xml --github-output ${GO_E2E_DIRS}

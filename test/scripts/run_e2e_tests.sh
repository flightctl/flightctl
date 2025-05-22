#!/usr/bin/env bash
set -x -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

REPORTS=${1}
GO_E2E_DIRS=${@:2}
GINKGO_FOCUS=${GINKGO_FOCUS:-""}
GINKGO_LABEL_FILTER=${GINKGO_LABEL_FILTER:-"sanity"}
FOCUS_FLAG=""


go install github.com/onsi/ginkgo/v2/ginkgo

GOBIN=$(go env GOBIN)

export API_ENDPOINT=https://$(get_endpoint_host flightctl-api-route)
export REGISTRY_ENDPOINT=$(registry_address)

if [[ "${GINKGO_FOCUS}" != "" ]]; then
	"${GOBIN}/ginkgo" run --focus "${GINKGO_FOCUS}" --label-filter "${GINKGO_LABEL_FILTER}" --timeout 60m --race -vv --junit-report ${REPORTS}/junit_e2e_test.xml --github-output ${GO_E2E_DIRS}
else
	"${GOBIN}/ginkgo" run --label-filter "${GINKGO_LABEL_FILTER}" --timeout 60m --race -vv --junit-report ${REPORTS}/junit_e2e_test.xml --github-output ${GO_E2E_DIRS}
fi

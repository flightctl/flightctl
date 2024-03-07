#!/usr/bin/env bash

set -euo pipefail

if ! [[ "$0" =~ hack/mockgen.sh ]]; then
  echo "must be run from repository root"
  exit 255
fi

# ensure mockgen is installed
go install -v go.uber.org/mock/mockgen@v0.4.0

# file format '=' delimited: source=destination
mock_list_file="hack/mock.list.txt"

while IFS= read -r line; do
  IFS='=' read -r source destination <<< "${line}"
  # generate mock
  mockgen \
    -source="${source}" \
    -destination="${destination}" \
    -package=$(basename "$(dirname "$destination")") \

  echo "Generating ${destination}..."
done < "$mock_list_file"

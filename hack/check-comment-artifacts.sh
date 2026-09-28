#!/usr/bin/env bash
# Wrapper so CI/Make can invoke the comment-artifact check like other hack/*.sh tools.
set -o errexit
set -o pipefail
set -o nounset

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "${__dir}/check_comment_artifacts.py" "$@"

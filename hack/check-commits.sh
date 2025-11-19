#!/usr/bin/env bash

set -o errexit
set -o pipefail
set -o nounset

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

remote=$(git remote -v | (grep "github\.com.flightctl/flightctl" || true) | head -n 1 | awk '{ print $1 }')
if [ -z $remote ]; then
    echo "could not find remote for github.com/flightctl/flightctl"
    exit 1
fi

# Detect the target branch for comparison
current_branch="$(git rev-parse --abbrev-ref HEAD)"

# Use GitHub Actions target branch if available, otherwise default to main
if [ -n "${GITHUB_BASE_REF:-}" ]; then
    # GitHub Actions provides the target branch in GITHUB_BASE_REF
    # For PR: gshilin-sdb wants to merge 1 commit into flightctl:release-0.10
    # GITHUB_BASE_REF will be "release-0.10"
    target_branch="$GITHUB_BASE_REF"
    echo "Using GitHub Actions target branch: $target_branch"
else
    # Default to main for local development or non-GitHub environments
    target_branch="main"
    echo "Using default target branch: $target_branch"
fi

master_branch="$remote/$target_branch"

echo "Checking commits between ${master_branch} and ${current_branch}"

revs=$(git rev-list "${master_branch}".."${current_branch}")

for commit in ${revs};
do
    commit_message=$(git cat-file commit ${commit} | sed '1,/^$/d')
    tmp_commit_file="$(mktemp)"
    echo "${commit_message}" > ${tmp_commit_file}
    ${__dir}/check-commit-message.sh "${tmp_commit_file}"
done


exit 0

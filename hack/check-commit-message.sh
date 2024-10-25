#!/usr/bin/env bash

set -o nounset

commit_file=${1}
commit_message="$(cat ${commit_file})"
valid_commit_regex='^(NO-ISSUE|Merge|Revert|([A-Z]+-[0-9]+:))'
valid_github_regex='^((Fixes|fixes|Closes|closes|Issue|issue) #[0-9]+)'

error_msg="""Aborting commit: ${commit_message}
---
Your commit message should start with a JIRA issue ('JIRA-1111') or a GitHub issue ('Fixes #39')
with a following colon(:).
i.e. 'MGMT-42: Summary of the commit message'
You can also ignore the ticket checking with 'NO-ISSUE' for main only.

Your message is preserved at '${commit_file}'
"""

status=$(echo "${commit_message}" | grep -qE "${valid_commit_regex}")

if [ $? -gt 0 ]; then
    status=$(echo "${commit_message}" | grep -qE "${valid_github_regex}")
    if [ $? -gt 0 ]; then
        echo "${error_msg}"
        exit 1
    fi
fi

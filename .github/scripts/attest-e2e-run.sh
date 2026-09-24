#!/usr/bin/env bash

set -euo pipefail

reject() {
  local reason="$1"
  echo "::notice::No reusable E2E proof: ${reason}"
  {
    echo "## E2E proof attestation"
    echo "No proof issued: ${reason}."
  } >> "$GITHUB_STEP_SUMMARY"
  echo "attested=false" >> "$GITHUB_OUTPUT"
  exit 0
}

repo="$GITHUB_REPOSITORY"
run_id="$SOURCE_RUN_ID"
run_attempt="$SOURCE_RUN_ATTEMPT"

if [[ "$SOURCE_EVENT" != "pull_request" || "$SOURCE_CONCLUSION" != "success" ]]; then
  reject "the source run was not a successful pull request workflow"
fi

if ! source_run=$(gh api "repos/${repo}/actions/runs/${run_id}"); then
  reject "the source workflow run could not be verified"
fi

if ! jq -e \
  --arg repo "$repo" \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg head_sha "$SOURCE_HEAD_SHA" \
  --arg head_branch "$SOURCE_HEAD_BRANCH" \
  --arg head_repository "$SOURCE_HEAD_REPOSITORY" '
    .event == "pull_request" and
    .status == "completed" and
    .conclusion == "success" and
    .repository.full_name == $repo and
    (.path == ".github/workflows/pr-e2e-testing.yaml" or
     (.path | startswith(".github/workflows/pr-e2e-testing.yaml@"))) and
    (.id | tostring) == $run_id and
    (.run_attempt | tostring) == $run_attempt and
    .head_sha == $head_sha and
    .head_branch == $head_branch and
    .head_repository.full_name == $head_repository
  ' <<< "$source_run" > /dev/null; then
  reject "the source workflow metadata did not match the completed run"
fi

if [[ "$SOURCE_HEAD_REPOSITORY" != */* ]]; then
  reject "the source repository could not be identified"
fi
head_owner="${SOURCE_HEAD_REPOSITORY%%/*}"

if ! pull_requests=$(gh api --method GET -f state=open -f head="${head_owner}:${SOURCE_HEAD_BRANCH}" \
  "repos/${repo}/pulls?per_page=100"); then
  reject "the source pull request could not be found"
fi

if ! default_branch=$(gh api "repos/${repo}" --jq '.default_branch'); then
  reject "the repository default branch could not be read"
fi

matching_pull_requests=$(jq -c \
  --arg repo "$repo" \
  --arg head_sha "$SOURCE_HEAD_SHA" \
  --arg head_branch "$SOURCE_HEAD_BRANCH" \
  --arg head_repository "$SOURCE_HEAD_REPOSITORY" \
  --arg default_branch "$default_branch" '
    [ .[] | select(
      .state == "open" and
      .base.repo.full_name == $repo and
      .base.ref == $default_branch and
      .head.repo.full_name == $head_repository and
      .head.ref == $head_branch and
      .head.sha == $head_sha and
      any(.labels[]?.name; . == "run-e2e")
    ) ]
  ' <<< "$pull_requests")

if [[ "$(jq 'length' <<< "$matching_pull_requests")" != "1" ]]; then
  reject "there was not exactly one open, labeled PR for the source run"
fi
pull_request=$(jq -c '.[0]' <<< "$matching_pull_requests")
pr_number=$(jq -r '.number' <<< "$pull_request")
pr_base_sha=$(jq -r '.base.sha' <<< "$pull_request")
pr_head_sha=$(jq -r '.head.sha' <<< "$pull_request")
pr_merge_sha=$(jq -r '.merge_commit_sha // empty' <<< "$pull_request")

repo_owner="${repo%%/*}"
repo_name="${repo#*/}"
if ! changed_file_count=$(gh api graphql \
  -F owner="$repo_owner" \
  -F name="$repo_name" \
  -F number="$pr_number" \
  -f query='query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){changedFiles}}}' \
  --jq '.data.repository.pullRequest.changedFiles'); then
  reject "the source pull request change count could not be verified"
fi
if [[ ! "$changed_file_count" =~ ^[0-9]+$ ]] || (( changed_file_count > 3000 )); then
  reject "the source pull request file diff exceeds the verifiable GitHub API limit"
fi

# The REST files endpoint stops at 3000 paths. The count check above makes a
# complete paginated response safe to use for rejecting any .github changes.
if ! changed_files=$(gh api --paginate --jq \
  '.[].filename, .[].previous_filename // empty' \
  "repos/${repo}/pulls/${pr_number}/files?per_page=100"); then
  reject "the source pull request file list could not be verified"
fi
if grep -q '^\.github/' <<< "$changed_files"; then
  reject "the source pull request changed trusted workflow or action files"
fi

if ! job_results=$(gh api --paginate --jq \
  '.jobs[] | select(.name == "e2e-tests" or .name == "api-tests") | [.name, .conclusion] | @tsv' \
  "repos/${repo}/actions/runs/${run_id}/jobs?filter=latest&per_page=100"); then
  reject "the source E2E and API job results could not be verified"
fi
e2e_result=$(awk -F '\t' '$1 == "e2e-tests" { print $2 }' <<< "$job_results")
api_result=$(awk -F '\t' '$1 == "api-tests" { print $2 }' <<< "$job_results")
if [[ "$e2e_result" != "success" || "$api_result" != "success" ]]; then
  reject "the source E2E matrix and API jobs did not both pass"
fi

candidate_name=""
if ! candidate_names=$(gh api --paginate --jq \
  '.artifacts[] | select(.expired == false) | select(.name | startswith("e2e-candidate-tree-v2-")) | .name' \
  "repos/${repo}/actions/runs/${run_id}/artifacts?per_page=100"); then
  reject "the source E2E proof candidate could not be located"
fi
if [[ -z "$candidate_names" ]]; then
  reject "the source run did not contain an E2E proof candidate"
fi
mapfile -t candidate_names_array <<< "$candidate_names"
if [[ "${#candidate_names_array[@]}" != "1" ]]; then
  reject "the source run did not contain exactly one proof candidate"
fi
candidate_name="${candidate_names_array[0]}"

candidate_dir="${RUNNER_TEMP}/e2e-candidate-${run_id}"
mkdir -p "$candidate_dir"
if ! gh run download "$run_id" --repo "$repo" --name "$candidate_name" --dir "$candidate_dir"; then
  reject "the E2E proof candidate could not be downloaded"
fi
candidate_file=$(find "$candidate_dir" -type f -name e2e-candidate.json -print -quit)
if [[ ! -f "$candidate_file" ]]; then
  reject "the E2E proof candidate was missing its JSON record"
fi
if ! candidate=$(cat "$candidate_file") || ! jq -e \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg head_sha "$pr_head_sha" \
  --arg base_sha "$pr_base_sha" \
  --arg tested_sha "$pr_merge_sha" \
  --arg candidate_name "$candidate_name" '
    .version == 2 and
    .source_run_id == $run_id and
    .source_run_attempt == $run_attempt and
    .head_sha == $head_sha and
    .base_sha == $base_sha and
    .tested_sha == $tested_sha and
    (.tree | test("^[0-9a-f]{40}$")) and
    $candidate_name == ("e2e-candidate-tree-v2-" + .tree)
  ' <<< "$candidate" > /dev/null; then
  reject "the proof candidate did not match the tested commit and PR metadata"
fi
tree=$(jq -r '.tree' <<< "$candidate")

if [[ ! "$pr_merge_sha" =~ ^[0-9a-f]{40}$ ]]; then
  reject "GitHub did not provide a current PR merge commit"
fi
if ! tested_commit=$(gh api "repos/${repo}/commits/${pr_merge_sha}"); then
  reject "the tested PR merge commit could not be verified"
fi
if ! jq -e \
  --arg tree "$tree" \
  --arg base_sha "$pr_base_sha" \
  --arg head_sha "$pr_head_sha" '
    (.parents | map(.sha)) as $parents |
    .commit.tree.sha == $tree and
    ($parents | index($base_sha)) != null and
    ($parents | index($head_sha)) != null
  ' <<< "$tested_commit" > /dev/null; then
  reject "the candidate tree did not match the current PR merge commit"
fi

proof_file="e2e-proof.json"
jq -n \
  --arg tree "$tree" \
  --arg tested_sha "$pr_merge_sha" \
  --arg base_sha "$pr_base_sha" \
  --arg head_sha "$pr_head_sha" \
  --arg source_run_id "$run_id" \
  --arg source_run_attempt "$run_attempt" \
  --argjson source_pr_number "$pr_number" \
  --arg attestor_run_id "$GITHUB_RUN_ID" \
  '{version: 2, tree: $tree, tested_sha: $tested_sha, base_sha: $base_sha,
    head_sha: $head_sha, source_run_id: $source_run_id,
    source_run_attempt: $source_run_attempt, source_pr_number: $source_pr_number,
    e2e_result: "success", api_result: "success", attestor_run_id: $attestor_run_id}' \
  > "$proof_file"

{
  echo "attested=true"
  echo "tree=${tree}"
  echo "source_run=${run_id}"
  echo "source_pr=${pr_number}"
} >> "$GITHUB_OUTPUT"
{
  echo "## E2E proof attestation"
  echo "Issued trusted proof for PR [#${pr_number}](${GITHUB_SERVER_URL}/${repo}/pull/${pr_number}), tree \`${tree}\`."
  echo "Verified source E2E and API jobs in [run #${run_id}](${GITHUB_SERVER_URL}/${repo}/actions/runs/${run_id})."
} >> "$GITHUB_STEP_SUMMARY"

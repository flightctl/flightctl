# Backport labels (Phase 1)

Pull requests targeting **`main`** must declare backport intent using repository labels. CI enforces this via the **PR backport labels** workflow (job **Backport labels**).

## Rules

- **`backport: none`** — the change is not intended for backport to release branches.
- **`backport: release-<branch-suffix>`** — one or more labels, each naming a **`release-*` branch** on the remote. The label text after `backport: ` must be exactly the branch name (e.g. branch `release-1.1` → label `backport: release-1.1`).
- **Invalid:** no qualifying labels, or **`backport: none`** together with any **`backport: release-*`** label.

## Creating labels

Create labels in the GitHub UI (**Issues → Labels**) or with the CLI:

```bash
# Required opt-out label (create once)
gh label create "backport: none" --color "BFD4F2" --description "PR is not intended for backport to release branches"

# For each active release branch (repeat when a new release line is cut)
BRANCH=release-1.1
gh label create "backport: ${BRANCH}" --color "0E8A16" --description "Backport target: ${BRANCH}"
```

When you add a new **`release-*`** branch, add a matching **`backport: release-...`** label at the same time.

## Branch protection and merge queue

After the workflow exists on **`main`**, require it in repository settings:

1. **Settings → Rules → Rulesets** (or **Branches → Branch protection rules**), for **`main`**.
2. Enable **Require status checks to pass**.
3. Add the check whose name matches the workflow job: **`Backport labels`** (full UI path is often **PR backport labels / Backport labels**).

If you use a **merge queue**, include the same check in the merge queue’s required checks. The workflow runs on **`merge_group`** with a no-op step so the check stays green in the queue (labels are validated on the PR before queueing).

Until this check is required in branch or ruleset settings, the workflow still runs on PRs for visibility, but merges are not blocked—**configure branch protection after this workflow is on `main`**.

This repository had no prior branch protection when this was added; enabling rules is a **maintainer action in GitHub** (this cannot be fully expressed in-repo without org-wide ruleset automation).

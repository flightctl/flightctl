# Backporting commits

All PRs for new functionality and bug fixes should be made to the `main` branch.

When a release X.Y enters the feature freeze stage and a new release is being tested and prepared, a
new release branch of the form `release-X.Y` will be created.

From `main`, commits can be backported to the appropriate release branch(es), generally **by the
developer who authored the commits**. Backports should be done as a PR and not directly to the
release branch, to ensure proper test verification of the cherry-picked commits.

To make this easy and standardized there is a script
[`hack/backport-from-pr`](../../hack/backport-from-pr) in the root of this repo. It will figure out
which commits in the `main` branch are associated with a particular PR and cherry-pick them to a
separate branch that is based on the specified release branch. It will then make a PR to do the
backport and add the appropriate labels.

For example, to backport PR #432 to the `release-1.1` branch:

```sh
$ ./hack/backport-from-pr --release 1.1 432
```

A link to the new backport PR will be printed to the console.

This assumes the git remote for the upstream repo is called the conventional `origin`, otherwise you
need to specify the `--remote` flag.

## Labeling

The original feature/bug PRs should have a label `release-<release>` (e.g.
`release-1.1`) to indicate that it is intended for a particular release. This label must be present
for the above script to be willing to backport it for a particular release.

The backport PRs themselves should have the label `backport-for-<release>` (e.g. `backport-for-1.1`)
to make it easy to determine if there are any outstanding backport PRs open before cutting a
release. The script above will automatically add this label to the backport PRs it creates.

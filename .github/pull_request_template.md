## Backport intent (required before merge to `main`)

Set **either**:

- [ ] **`backport: none`** — change is not intended for stable release branches, **or**
- [ ] **One or more `backport: release-*` labels** — each label must match an existing `release-*` branch name (example: branch `release-1.1` → label `backport: release-1.1`).

Do not combine `backport: none` with any `backport: release-*` label.

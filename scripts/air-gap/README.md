# Air-gap Image Mirroring — `mirror-images.sh`

Enumerates all RHEM container images for a given deployment variant and
generates `skopeo copy` commands ready to run against a local registry in an
air-gapped environment.

Related Jira stories: **EDM-3957** (CLI scaffold), **EDM-3958** (helm-chart-opts parsing),
**EDM-3959** (observability images), **EDM-3960** (artifact manifest).

---

## Prerequisites

| Tool | Version | Required for |
|------|---------|-------------|
| `yq` (mikefarah) | v4+ | YAML parsing — always required |
| `skopeo` | any | Only when using `--execute`; script still prints commands without it |

Install `yq` on a connected RHEL system:

```bash
dnf install yq
```

Or download the binary directly (no root required):

```bash
curl -sL https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 \
    -o /usr/local/bin/yq && chmod +x /usr/local/bin/yq
```

---

## Usage

Run from the flightctl repository root.

```
./scripts/air-gap/mirror-images.sh --variant <variant> --dest-registry <host:port> [OPTIONS]
```

### Required flags

| Flag | Description |
|------|-------------|
| `--variant` | One of: `community-el9`, `community-el10`, `redhat-el9`, `redhat-el10` |
| `--dest-registry` | Destination registry — no scheme, no trailing slash. Example: `local-registry.example.com:5000` |

### Options

| Flag | Description |
|------|-------------|
| `--execute` | Execute `skopeo copy` commands immediately in addition to printing them. Requires `skopeo` and a reachable destination registry. Without this flag the script is always a safe dry-run. |
| `--help`, `-h` | Print usage and exit. |

### Output

| Stream / File | Content |
|---------------|---------|
| `stdout` | One `skopeo copy` command per image — pipe-safe, no log noise |
| `stderr` | Progress logs (`[INFO]`, `[WARN]`, `[ERROR]`) |
| `artifact-manifest-<variant>.yaml` | Machine-readable manifest listing all images, RPM dependencies, and reserved catalog fields |

---

## Examples

### 1 — Dry-run: print all mirror commands for the community (upstream) RHEL 9 variant

```bash
./scripts/air-gap/mirror-images.sh \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000
```

Sample output on `stdout`:

```
skopeo copy --all docker://quay.io/flightctl/flightctl-api-el9:latest docker://local-registry.example.com:5000/flightctl/flightctl-api-el9:latest
skopeo copy --all docker://quay.io/sclorg/postgresql-16-c9s:20250214 docker://local-registry.example.com:5000/sclorg/postgresql-16-c9s:20250214
...
```

### 2 — Save the commands to a file for later use (offline / portable media)

```bash
./scripts/air-gap/mirror-images.sh \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    > mirror-commands-redhat-el9.sh

chmod +x mirror-commands-redhat-el9.sh
```

Transfer `mirror-commands-redhat-el9.sh` to the connected preparation system, then run it.

### 3 — Execute mirroring immediately

```bash
./scripts/air-gap/mirror-images.sh \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --execute
```

### 4 — Pipe stdout directly to bash (equivalent to `--execute`)

```bash
./scripts/air-gap/mirror-images.sh \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    | bash
```

### 5 — Inspect the generated artifact manifest

```bash
./scripts/air-gap/mirror-images.sh \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000

# Manifest is written to the current directory:
cat artifact-manifest-community-el9.yaml

# Count images with yq:
yq e '.images | length' artifact-manifest-community-el9.yaml

# List RPM dependencies:
yq e '.rpms[]' artifact-manifest-community-el9.yaml
```

---

## Image sources

The script pulls image references from two YAML files in the repository:

| Source | Path | Content |
|--------|------|---------|
| Helm chart options | `deploy/helm/helm-chart-opts.yaml` | All FlightControl component images, keyed by variant |
| Observability images | `packaging/images/el9/images.yaml` (or `el10/`) | Images installed by the `flightctl-observability` RPM — **not** in the Helm chart |

Images that appear in both files with the same `image:tag` pair are deduplicated — each unique source reference appears exactly once in the output.

### Tag fallback

Core FlightControl service images (`api`, `worker`, `periodic`, etc.) have no explicit `tag:` field in `helm-chart-opts.yaml`. The script reads `appVersion` from `deploy/helm/flightctl/Chart.yaml` and uses that as the fallback tag. On a release-tagged checkout this will be the release version; on a development branch it is typically `latest`.

---

## Variant → registry mapping

| Variant | Source registries |
|---------|------------------|
| `community-el9` / `community-el10` | `quay.io`, `docker.io` |
| `redhat-el9` / `redhat-el10` | `registry.redhat.io` (requires pull secret) |

For Red Hat variants, the connected preparation system needs valid credentials for `registry.redhat.io` before running `skopeo copy`.

---

## Testing

Tests use [bats-core](https://github.com/bats-core/bats-core) and `yq`. Both must be available before running.

### Install bats (no root required)

```bash
git clone --depth 1 https://github.com/bats-core/bats-core.git /tmp/bats-core
export PATH="/tmp/bats-core/bin:${PATH}"
bats --version   # should print "Bats 1.x.x"
```

### Run all tests

```bash
# From the repository root:
bats scripts/air-gap/test/mirror-images.bats
```

Expected output:

```
1..33
ok 1 [unit] --help prints usage and exits 0
ok 2 [unit] missing --variant exits non-zero with error message
...
ok 33 [integration] stdout is clean — no [INFO]/[WARN]/[ERROR] lines mixed in
```

### Run only unit tests (no live registry needed)

```bash
bats --filter unit scripts/air-gap/test/mirror-images.bats
```

### Run only integration tests (use the real repo YAML files)

```bash
bats --filter integration scripts/air-gap/test/mirror-images.bats
```

Integration tests are skipped automatically if `deploy/helm/helm-chart-opts.yaml` is absent (e.g., a shallow clone).

### Test structure

```
scripts/air-gap/
├── mirror-images.sh             # Production script
└── test/
    ├── fixtures/
    │   ├── helm-chart-opts.yaml # Minimal variant fixture (community-el9, redhat-el9)
    │   ├── images-el9.yaml      # Minimal observability fixture for el9
    │   ├── images-el10.yaml     # Minimal observability fixture for el10
    │   └── Chart.yaml           # Fixture with appVersion: v0.99.0-test
    └── mirror-images.bats       # 28 unit tests + 5 integration tests
```

Unit tests source the script (the `BASH_SOURCE` guard prevents `main` from running) and
override path constants via environment variables — no repository files are touched.

### What is tested

| Category | What is covered |
|----------|----------------|
| CLI / arg parsing | `--help`, missing flags, invalid variant, all four valid variants |
| `get_app_version` | Reads `appVersion` from fixture Chart.yaml; fails on missing file |
| `parse_helm_chart_opts` | Explicit tags, `appVersion` fallback, variant filtering, missing-file error |
| `image_to_dest` | Registry stripping, single-component paths, multi-component paths |
| `parse_observability_images` | el9/el10 file selection, line count, missing-file warning |
| Deduplication | Overlapping images across sources; identical source:tag pairs collapsed to one line |
| Skopeo format | `--all` flag, `docker://` transport, destination registry in every dest field |
| Artifact manifest | File created, top-level keys present, variant matches flag, image count consistent |
| Integration | Real `helm-chart-opts.yaml`, `registry.redhat.io` sources, stdout cleanliness |

---

## Troubleshooting

**`yq is required but not installed`**
Install `yq` v4 (mikefarah) — see [Prerequisites](#prerequisites).

**`Required file not found: deploy/helm/helm-chart-opts.yaml`**
Run the script from the flightctl repository root, or run `git pull` to fetch the latest files.

**`Invalid variant 'x'`**
Use one of: `community-el9`, `community-el10`, `redhat-el9`, `redhat-el10`.

**`registry.redhat.io` images fail during `--execute`**
Log in first: `podman login registry.redhat.io` or configure a pull secret for `skopeo`.

**`appVersion is 'latest'` warning**
The repo is not on a release tag. Mirror commands will use `:latest` for untagged images, which may not be reproducible. Check out a release tag for a pinned manifest.

# Air-gap Image Mirroring

Enumerates all RHEM container images for a given deployment variant and
generates `skopeo copy` commands ready to run against a local registry in an
air-gapped environment.

Two implementations are available and produce identical output:

| Implementation | Path | Requirements |
|---------------|------|-------------|
| **Go tool** (recommended) | `scripts/air-gap/mirror-images/` | Go 1.24+ or `make build-mirror-images` |
| Bash script | `scripts/air-gap/mirror-images.sh` | `yq` v4+ (mikefarah) |

Related Jira stories: **EDM-3957** (CLI scaffold), **EDM-3958** (helm-chart-opts parsing),
**EDM-3959** (observability images), **EDM-3960** (artifact manifest).

---

## Quick start (Go tool)

```bash
# Build
make build-mirror-images

# Dry-run: print all skopeo commands for the community RHEL 9 variant
./bin/mirror-images --variant community-el9 --dest-registry local-registry.example.com:5000

# Execute: mirror images to a reachable registry
./bin/mirror-images --variant community-el9 --dest-registry local-registry.example.com:5000 --execute
```

---

## Prerequisites

### Go tool

| Tool | When required |
|------|--------------|
| Go 1.24+ (or `podman`) | Build time only |
| `skopeo` | Only when using `--execute` |

### Bash script

| Tool | Version | Required for |
|------|---------|-------------|
| `yq` (mikefarah) | v4+ | YAML parsing — always required |
| `skopeo` | any | Only when using `--execute` |

Install `yq` on a connected RHEL system:

```bash
dnf install yq
```

Or download the binary (no root required):

```bash
curl -sL https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 \
    -o /usr/local/bin/yq && chmod +x /usr/local/bin/yq
```

---

## Usage

Both implementations accept the same flags.

```
mirror-images --variant <variant> --dest-registry <host:port> [OPTIONS]
```

### Required flags

| Flag | Description |
|------|-------------|
| `--variant` | One of: `community-el9`, `community-el10`, `redhat-el9`, `redhat-el10` |
| `--dest-registry` | Destination registry — no scheme, no trailing slash. Example: `local-registry.example.com:5000` |

### Options

| Flag | Description |
|------|-------------|
| `--execute` | Execute `skopeo copy` commands immediately in addition to printing them. Requires `skopeo` and a reachable destination registry. Without this flag the tool is always a safe dry-run. |
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
# Go tool
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000

# Bash script
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

### 2 — Save commands to a file for offline use

```bash
./bin/mirror-images \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    > mirror-commands-redhat-el9.sh

chmod +x mirror-commands-redhat-el9.sh
```

Transfer `mirror-commands-redhat-el9.sh` to the connected preparation system, then run it.

### 3 — Execute mirroring immediately

```bash
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --execute
```

### 4 — Pipe stdout directly to bash (equivalent to `--execute`)

```bash
./bin/mirror-images \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    | bash
```

### 5 — Inspect the generated artifact manifest

```bash
./bin/mirror-images \
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

Both implementations pull image references from the same two YAML files:

| Source | Path | Content |
|--------|------|---------|
| Helm chart options | `deploy/helm/helm-chart-opts.yaml` | All FlightControl component images, keyed by variant |
| Observability images | `packaging/images/el9/images.yaml` (or `el10/`) | Images installed by the `flightctl-observability` RPM — **not** in the Helm chart |

Images that appear in both files with the same `image:tag` pair are deduplicated — each unique source reference appears exactly once in the output.

### Tag fallback

Core FlightControl service images (`api`, `worker`, `periodic`, etc.) have no explicit `tag:` field in `helm-chart-opts.yaml`. The tool reads `appVersion` from `deploy/helm/flightctl/Chart.yaml` and uses that as the fallback tag. On a release-tagged checkout this will be the release version; on a development branch it is typically `latest`.

---

## Variant → registry mapping

| Variant | Source registries |
|---------|------------------|
| `community-el9` / `community-el10` | `quay.io`, `docker.io` |
| `redhat-el9` / `redhat-el10` | `registry.redhat.io` (requires pull secret) |

For Red Hat variants, the connected preparation system needs valid credentials for `registry.redhat.io` before running `skopeo copy`.

---

## Go tool — code walkthrough

The Go tool lives at `scripts/air-gap/mirror-images/` and is organized into four files:

```
scripts/air-gap/mirror-images/
├── main.go      — CLI entry point, flag validation, workflow orchestration
├── parser.go    — YAML parsing for all four input files
├── mirror.go    — image deduplication, destination path calculation, skopeo execution
└── manifest.go  — artifact manifest YAML generation
```

### main.go — entry point and orchestration

`main.go` wires up the [cobra](https://github.com/spf13/cobra) CLI and drives the six-step workflow in `RunE`:

1. Validate `--variant` and `--dest-registry` flags.
2. Resolve input file paths (supports env var overrides for testing).
3. Call `ReadAppVersion` to get the chart `appVersion` (used as fallback tag).
4. Call `ParseHelmChartOpts` and `ParseObsImages` to collect all image references.
5. Deduplicate and call `GenerateCommands` (print + optionally execute skopeo).
6. Call `WriteManifest` to write `artifact-manifest-<variant>.yaml`.

**Path resolution** — the binary is expected to live in `bin/`, one level below the repo root. `repoRoot()` walks up with `os.Executable()` + `filepath.EvalSymlinks` so paths resolve correctly regardless of where you invoke the binary. Every path also supports an environment variable override:

| Env var | Default path |
|---------|-------------|
| `HELM_CHART_OPTS` | `deploy/helm/helm-chart-opts.yaml` |
| `CHART_YAML` | `deploy/helm/flightctl/Chart.yaml` |
| `OBS_IMAGES_EL9` | `packaging/images/el9/images.yaml` |
| `OBS_IMAGES_EL10` | `packaging/images/el10/images.yaml` |
| `RPM_SPEC` | `packaging/rpm/flightctl.spec` |

### parser.go — YAML input parsing

Parses four different YAML files into typed Go structs. No `yq` dependency.

**`ReadAppVersion(path)`**
Reads `appVersion` from `deploy/helm/flightctl/Chart.yaml`. Warns to stderr if the value is `"latest"` (non-reproducible).

**`ParseHelmChartOpts(path, variant, appVersion)`**
Reads `deploy/helm/helm-chart-opts.yaml`. The file is a map keyed by variant name; each variant contains an `images:` sub-map. Images without an explicit `tag:` field receive `appVersion` as the fallback tag.

Relevant struct layout:
```go
type HelmChartOpts map[string]ChartVariant

type ChartVariant struct {
    Images map[string]ImageSpec `yaml:"images"`
}

type ImageSpec struct {
    Image string `yaml:"image"`
    Tag   string `yaml:"tag"`
}
```

**`ParseObsImages(el9Path, el10Path, variant)`**
Selects `el9/images.yaml` or `el10/images.yaml` based on the variant name. A missing file is a non-fatal warning (observability images are optional). Images from `registry.redhat.io` trigger an additional warning about pull credentials.

**`ParseRPMRequires(specPath)`**
Scans `packaging/rpm/flightctl.spec` line by line with `bufio.Scanner`. Collects all `Requires:` lines (not `BuildRequires:`), strips version constraints (`>= 1.1` etc.), excludes file-path dependencies (`/bin/bash` etc.), then returns a sorted, deduplicated list.

### mirror.go — image processing and skopeo execution

**`ImagePair`** — the central data type passed between all stages:
```go
type ImagePair struct {
    Source string  // "quay.io/flightctl/flightctl-api-el9:latest"
    Dest   string  // "localhost:5000/flightctl/flightctl-api-el9:latest"
}
```

**`ImageToDest(image, tag)`**
Strips the source registry hostname (everything up to the first `/`) and prepends `destRegistry`. The package-level `var destRegistry string` (set in `main.go`'s `RunE`) makes the signature simple and mirrors the bash script's global `$DEST_REGISTRY`.

```
"quay.io/flightctl/flightctl-api-el9" + "latest"
    → "localhost:5000/flightctl/flightctl-api-el9:latest"
```

**`Dedup(pairs)`**
Removes `ImagePair` entries with duplicate `Source` values using a `map[string]struct{}` seen-set, preserving first-occurrence order.

**`GenerateCommands(ctx, pairs, execute, exec)`**
Prints one `skopeo copy --all docker://src docker://dst` line per pair to **stdout** (pipe-safe). All progress logs go to **stderr**. When `execute` is true, runs each command via `exec.ExecuteWithContext`. Skopeo failures are non-fatal — the loop logs a warning and continues so one unavailable image does not abort an entire mirror run.

### manifest.go — artifact manifest output

**`WriteManifest(variant, pairs, rpms)`**
Builds a `Manifest` struct, marshals it with `gopkg.in/yaml.v3`, prepends a human-readable comment header, and writes `artifact-manifest-<variant>.yaml` to the current working directory.

Manifest structure:
```yaml
# RHEM Air-gapped Installation Artifact Manifest
# ...
metadata:
  variant: community-el9
  generated_at: "2025-01-15T10:30:00Z"
  tool_version: "2.0.0"
images:
  - source: quay.io/flightctl/flightctl-api-el9:latest
    destination: localhost:5000/flightctl/flightctl-api-el9:latest
rpms:
  - flightctl-agent
  - flightctl-cli
  - ...
catalogs: []  # reserved for future use
```

**`buildRPMList(specRPMs)`**
Prepends five well-known top-level packages (`flightctl-cli`, `flightctl-agent`, `flightctl-selinux`, `flightctl-services`, `flightctl-observability`) that are not listed as `Requires:` of themselves in the spec file, then appends the transitive runtime dependencies parsed from the spec, skipping duplicates.

---

## Building the Go tool

```bash
# Via make (recommended — respects GOENV, GOOS, GOARCH, GO_BUILD_FLAGS)
make build-mirror-images

# Directly with go build
go build -o bin/mirror-images ./scripts/air-gap/mirror-images

# Via podman (no local Go toolchain needed)
podman run --rm \
    -v $(pwd):/workspace:z \
    -w /workspace \
    golang:1.24 \
    go build -buildvcs=false -o bin/mirror-images ./scripts/air-gap/mirror-images
```

---

## Testing the Go tool

The Go tool's testability relies on the same env var path overrides used by the bash script's unit tests. Set the env vars to point at the fixture files under `scripts/air-gap/test/fixtures/` to run the tool without touching real repo files.

### Manual smoke test

```bash
# Build
make build-mirror-images

# 1. Help text
./bin/mirror-images --help

# 2. Dry-run — stdout must contain only skopeo commands
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry localhost:5000

# 3. Stdout cleanliness — should produce no output (no [INFO]/[WARN] lines)
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry localhost:5000 2>/dev/null \
    | grep -v "^skopeo"

# 4. Image count — expect 21 for community-el9 on the current repo
yq e '.images | length' artifact-manifest-community-el9.yaml

# 5. Red Hat variant uses registry.redhat.io sources
./bin/mirror-images \
    --variant redhat-el9 \
    --dest-registry localhost:5000 2>/dev/null \
    | grep registry.redhat.io

# 6. Invalid variant exits 1
./bin/mirror-images --variant bad --dest-registry localhost:5000
echo "exit: $?"

# 7. URL scheme in --dest-registry is rejected
./bin/mirror-images --variant community-el9 --dest-registry https://localhost:5000
echo "exit: $?"
```

### Fixture-based test (isolated from real repo files)

```bash
export HELM_CHART_OPTS=scripts/air-gap/test/fixtures/helm-chart-opts.yaml
export CHART_YAML=scripts/air-gap/test/fixtures/Chart.yaml
export OBS_IMAGES_EL9=scripts/air-gap/test/fixtures/images-el9.yaml
export OBS_IMAGES_EL10=scripts/air-gap/test/fixtures/images-el10.yaml
export RPM_SPEC=/dev/null   # no RPM parsing needed for image tests

./bin/mirror-images --variant community-el9 --dest-registry localhost:5000
```

The fixture `Chart.yaml` pins `appVersion: "v0.99.0-test"` so tagless images resolve deterministically in tests.

---

## Testing the Bash script

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
├── mirror-images.sh             # Production bash script
├── mirror-images/               # Go tool (drop-in replacement)
│   ├── main.go
│   ├── parser.go
│   ├── mirror.go
│   └── manifest.go
└── test/
    ├── fixtures/
    │   ├── helm-chart-opts.yaml # Minimal variant fixture (community-el9, redhat-el9)
    │   ├── images-el9.yaml      # Minimal observability fixture for el9
    │   ├── images-el10.yaml     # Minimal observability fixture for el10
    │   └── Chart.yaml           # Fixture with appVersion: v0.99.0-test
    └── mirror-images.bats       # 28 unit tests + 5 integration tests
```

Unit tests source the bash script (the `BASH_SOURCE` guard prevents `main` from running) and
override path constants via environment variables — no repository files are touched.

### What is tested (bash script)

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

**`yq is required but not installed`** (bash script only)
Install `yq` v4 (mikefarah) — see [Prerequisites](#prerequisites).

**`required file not found: deploy/helm/helm-chart-opts.yaml`**
Run the tool from the flightctl repository root, or run `git pull` to fetch the latest files.

**`invalid variant 'x'`**
Use one of: `community-el9`, `community-el10`, `redhat-el9`, `redhat-el10`.

**`registry.redhat.io` images fail during `--execute`**
Log in first: `podman login registry.redhat.io` or configure a pull secret for `skopeo`.

**`appVersion is 'latest'` warning**
The repo is not on a release tag. Mirror commands will use `:latest` for untagged images, which may not be reproducible. Check out a release tag for a pinned manifest.

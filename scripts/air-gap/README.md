# Air-gap Image Mirroring — `mirror-images`

Enumerates all RHEM container images for a given deployment variant and
generates `skopeo copy` commands ready to run against a local registry in an
air-gapped environment.

Related Jira stories: **EDM-3957** (CLI scaffold), **EDM-3958** (helm-chart-opts parsing),
**EDM-3959** (RPM-only images), **EDM-3960** (artifact manifest).

---

## Quick start

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

| Tool | When required |
|------|--------------|
| Go 1.24+ (or `podman`) | Build time only |
| `skopeo` | Only when using `--execute` |

---

## Usage

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
| `--insecure` | Add `--dest-tls-verify=false` to every `skopeo copy` command. Required when the destination registry serves plain HTTP instead of HTTPS. |
| `--help`, `-h` | Print usage and exit. |

### Output

| Stream / File | Content |
|---------------|---------|
| `stdout` | One `skopeo copy` command per image — pipe-safe, no log noise |
| `stderr` | Progress logs (`[INFO]`, `[WARN]`, `[ERROR]`) |
| `artifact-manifest-<variant>.yaml` | Machine-readable manifest listing all images and RPM dependencies |

---

## Examples

### 1 — Dry-run: print all mirror commands

```bash
./bin/mirror-images \
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

### 4 — Execute mirroring to an HTTP (non-TLS) registry

```bash
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry localhost:5000 \
    --execute \
    --insecure
```

### 5 — Pipe stdout directly to bash (equivalent to `--execute`)

```bash
./bin/mirror-images \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    | bash
```

### 6 — Inspect the generated artifact manifest

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

The tool reads image references from two sources per run:

| Source | Path | Content |
|--------|------|---------|
| Helm chart options | `deploy/helm/helm-chart-opts.yaml` | All FlightControl Helm component images, keyed by variant |
| RPM-only images (community-el9) | `packaging/images/el9/images.yaml` | Images installed by flightctl RPMs that are **not** in the Helm chart (pam-issuer, userinfo-proxy, grafana, prometheus) — community registry sources |
| RPM-only images (community-el10) | `packaging/images/el10/images.yaml` | Same set of RPM-only images for the el10 community variant |
| RPM-only images (redhat-el9) | `packaging/images/rhel9/images.yaml` | Downstream `registry.redhat.io` equivalents of the RPM-only images for the redhat-el9 variant |
| RPM-only images (redhat-el10) | `packaging/images/rhel10/images.yaml` | Downstream `registry.redhat.io` equivalents of the RPM-only images for the redhat-el10 variant |

The tool selects exactly one RPM images file per run based on the variant. Images that appear in both sources with the same `image:tag` are deduplicated — each unique source reference appears exactly once in the output.

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

## Code walkthrough

The tool lives at `scripts/air-gap/mirror-images/` and is organized into four files:

```
scripts/air-gap/mirror-images/
├── main.go      — CLI entry point, flag validation, workflow orchestration
├── parser.go    — YAML parsing for all input files
├── mirror.go    — image deduplication, destination path calculation, skopeo execution
└── manifest.go  — artifact manifest YAML generation
```

### main.go — entry point and orchestration

Wires up the [cobra](https://github.com/spf13/cobra) CLI and drives the six-step workflow in `RunE`:

1. Validate `--variant` and `--dest-registry` flags.
2. Resolve input file paths (supports env var overrides for testing).
3. Call `ReadAppVersion` to get the chart `appVersion` (used as fallback tag).
4. Call `ParseHelmChartOpts` and `ParseObsImages` to collect all image references.
5. Deduplicate and call `GenerateCommands` (print + optionally execute skopeo).
6. Call `WriteManifest` to write `artifact-manifest-<variant>.yaml`.

**Path resolution** — the binary is expected to live in `bin/`, one level below the repo root. `repoRoot()` walks up with `os.Executable()` + `filepath.EvalSymlinks`. Every path also supports an environment variable override for test isolation:

| Env var | Default path |
|---------|-------------|
| `HELM_CHART_OPTS` | `deploy/helm/helm-chart-opts.yaml` |
| `CHART_YAML` | `deploy/helm/flightctl/Chart.yaml` |
| `OBS_IMAGES_EL9` | `packaging/images/el9/images.yaml` |
| `OBS_IMAGES_EL10` | `packaging/images/el10/images.yaml` |
| `OBS_IMAGES_RHEL9` | `packaging/images/rhel9/images.yaml` |
| `OBS_IMAGES_RHEL10` | `packaging/images/rhel10/images.yaml` |
| `RPM_SPEC` | `packaging/rpm/flightctl.spec` |

### parser.go — YAML input parsing

Parses four different YAML files into typed Go structs. No external YAML tool dependency.

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

**`ParseObsImages(el9Path, el10Path, rhel9Path, rhel10Path, variant)`**
Selects one of four RPM-only image files based on the variant's distribution (community vs redhat) and OS version (el9 vs el10):

| Variant | File used |
|---------|-----------|
| `community-el9` | `packaging/images/el9/images.yaml` |
| `community-el10` | `packaging/images/el10/images.yaml` |
| `redhat-el9` | `packaging/images/rhel9/images.yaml` |
| `redhat-el10` | `packaging/images/rhel10/images.yaml` |

A missing file is a non-fatal warning. For community variants, images unexpectedly sourced from `registry.redhat.io` trigger a pull-credentials warning; for redhat variants all images are expected to come from `registry.redhat.io` so no warning is emitted.

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
Strips the source registry hostname (everything up to the first `/`) and prepends `destRegistry`:

```
"quay.io/flightctl/flightctl-api-el9" + "latest"
    → "localhost:5000/flightctl/flightctl-api-el9:latest"
```

**`Dedup(pairs)`**
Removes `ImagePair` entries with duplicate `Source` values, preserving first-occurrence order.

**`GenerateCommands(ctx, pairs, execute, insecure, exec)`**
Prints one `skopeo copy --all docker://src docker://dst` line per pair to **stdout** (pipe-safe). All progress logs go to **stderr**. When `insecure` is true, `--dest-tls-verify=false` is appended to every printed command and to the exec args, enabling pushes to HTTP registries. When `execute` is true, runs each command via `exec.ExecuteWithContext`. All images are attempted even when one fails so the operator gets a complete list of failures; if any copy fails, a summary error is returned after the loop so the process exits non-zero.

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
Prepends five well-known top-level packages (`flightctl-cli`, `flightctl-agent`, `flightctl-selinux`, `flightctl-services`, `flightctl-observability`) then appends the transitive runtime dependencies parsed from the spec, skipping duplicates.

---

## Building

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

## Testing

### Smoke test — all four variants

```bash
make build-mirror-images

# 1. Help text
./bin/mirror-images --help

# 2. Image count per variant
for v in community-el9 community-el10 redhat-el9 redhat-el10; do
    n=$(./bin/mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null | wc -l)
    echo "$v: $n images"
done

# 3. Community variants must NOT include registry.redhat.io sources
for v in community-el9 community-el10; do
    hits=$(./bin/mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null \
           | grep -c "registry.redhat.io" || true)
    echo "$v: $hits registry.redhat.io sources (expect 0)"
done

# 4. Redhat variants must NOT include quay.io or docker.io sources
for v in redhat-el9 redhat-el10; do
    hits=$(./bin/mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null \
           | grep -cE "quay\.io|docker\.io" || true)
    echo "$v: $hits quay.io/docker.io sources (expect 0)"
done

# 5. Redhat variants must include the 4 RPM-only downstream images
for v in redhat-el9 redhat-el10; do
    echo "=== $v RPM-only images ==="
    ./bin/mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null \
        | grep -E "pam-issuer|userinfo|grafana|prometheus"
done

# 6. Stdout is clean — no [INFO]/[WARN] lines mixed in
dirty=$(./bin/mirror-images --variant community-el9 --dest-registry localhost:5000 2>/dev/null \
        | grep -cE "^\[INFO\]|^\[WARN\]|^\[ERROR\]" || true)
echo "Dirty stdout lines: $dirty (expect 0)"

# 7. Invalid variant exits 1
./bin/mirror-images --variant bad --dest-registry localhost:5000 2>&1; echo "exit: $?"

# 8. URL scheme rejected
./bin/mirror-images --variant community-el9 --dest-registry https://localhost:5000 2>&1; echo "exit: $?"
```

### Fixture-based isolated test

Override the path env vars to point at minimal fixture files so the tool runs without touching live repo files:

```bash
export HELM_CHART_OPTS=scripts/air-gap/test/fixtures/helm-chart-opts.yaml
export CHART_YAML=scripts/air-gap/test/fixtures/Chart.yaml
export OBS_IMAGES_EL9=scripts/air-gap/test/fixtures/images-el9.yaml
export OBS_IMAGES_EL10=scripts/air-gap/test/fixtures/images-el10.yaml
export OBS_IMAGES_RHEL9=scripts/air-gap/test/fixtures/images-rhel9.yaml
export OBS_IMAGES_RHEL10=scripts/air-gap/test/fixtures/images-rhel10.yaml
export RPM_SPEC=/dev/null

./bin/mirror-images --variant community-el9 --dest-registry localhost:5000
```

---

## Troubleshooting

**`required file not found: deploy/helm/helm-chart-opts.yaml`**
Run the tool from the flightctl repository root, or run `git pull` to fetch the latest files.

**`invalid variant 'x'`**
Use one of: `community-el9`, `community-el10`, `redhat-el9`, `redhat-el10`.

**`registry.redhat.io` images fail during `--execute`**
Log in first: `podman login registry.redhat.io` or configure a pull secret for `skopeo`.

**`appVersion is 'latest'` warning**
The repo is not on a release tag. Mirror commands will use `:latest` for untagged images, which may not be reproducible. Check out a release tag for a pinned manifest.

**`N image(s) failed to copy` error**
One or more `skopeo copy` commands exited non-zero. The tool attempts all images before reporting — check the `[ERROR]` lines on stderr to identify which images failed and why.

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

### Build-time (one of the following)

You need either Go or Podman to compile the binary. Podman is the no-toolchain option — it pulls the official Go image and builds inside a container.

**Option A — Go 1.24+**

```bash
# RHEL 9 / CentOS Stream 9
sudo dnf install -y golang

# RHEL 10 / CentOS Stream 10 / Fedora
sudo dnf install -y golang

# Verify version (must be 1.24 or later)
go version
```

If the version in your distribution's repos is older than 1.24, install directly from upstream:

```bash
# Download and install Go 1.24 manually (adjust version/arch as needed)
curl -fsSL https://go.dev/dl/go1.24.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin   # add to ~/.bashrc to persist
go version
```

**Option B — Podman (no local Go toolchain needed)**

```bash
# RHEL 9 / CentOS Stream 9 / RHEL 10 / Fedora
sudo dnf install -y podman

# Verify
podman --version
```

### Runtime (required for `--execute`)

`skopeo` is only needed when you pass `--execute` to run the copy commands immediately. Without `--execute` the tool prints commands to stdout and skopeo is not called.

```bash
# RHEL 9 / CentOS Stream 9
sudo dnf install -y skopeo

# RHEL 10 / CentOS Stream 10 / Fedora
sudo dnf install -y skopeo

# Verify
skopeo --version
```

### Source checkout

```bash
# git is required to clone the repository
sudo dnf install -y git

# Clone the branch containing the tool
git clone --branch feat/EDM-3953-mirror-images-script \
    git@github.com:aekubacki/flightctl.git
cd flightctl
```

### Optional

`yq` is used in the examples to inspect the generated artifact manifest. It is not required to run the tool.

```bash
# Install yq via the upstream binary (recommended — distro packages lag behind)
sudo curl -fsSL \
    https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 \
    -o /usr/local/bin/yq
sudo chmod +x /usr/local/bin/yq

# Verify
yq --version
```

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
| `--tag-override <tag>` | Tag to use for flightctl service images (e.g. `v1.1.2`, `latest`). Overrides `appVersion` from `Chart.yaml`. Use to select a release version when running from a dev branch, or pass `latest` to force latest images on a release-tagged checkout. Third-party images with pinned versions (grafana, prometheus, etc.) are unaffected. |
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

### 4 — Match the installed RPM version (dev branch → versioned RPMs)

The most common `--tag-override` scenario: you are running `mirror-images` from a development branch (where `appVersion` in `Chart.yaml` is `"latest"`), but the RPMs installed on your edge nodes are a pinned release such as `v1.1.2`. The quadlet unit files shipped by those RPMs reference images tagged `:v1.1.2`, so a mirror built with `:latest` tags will not be found at runtime.

```bash
# Wrong — on a dev branch this mirrors everything as :latest,
# which won't match the v1.1.2 quadlet files on your edge nodes.
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000

# Correct — override the tag to match the installed RPM version.
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --tag-override v1.1.2
```

The tool will emit a `[WARN]` block on stderr when it detects this mismatch (dev branch + no `--tag-override`) so you know to re-run with the flag.

### 4b — Mirror a specific release from a release-tagged checkout

When you have the correct release tag checked out, `appVersion` in `Chart.yaml` already contains the version (e.g. `v1.1.2`) and `--tag-override` is not needed. Use it only when you want to target a *different* version than what the checkout declares — for example, pre-staging the next release while on a feature branch:

```bash
git checkout v1.1.2   # appVersion = v1.1.2 — no flag needed
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000

# Or, target a different version without switching branches:
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --tag-override v1.2.0
```

### 4c — Force `:latest` images on a release-tagged checkout

Occasionally you want the most recently built images even while standing on a release tag — for example, when testing a hotfix image pushed to `:latest` before a new tag is cut:

```bash
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --tag-override latest
```

**What `--tag-override` affects:**

The flag applies only to flightctl service images that have *no explicit tag* in their YAML source (`api`, `worker`, `periodic`, `pam-issuer`, `userinfo-proxy`, `db-setup`, etc.). Third-party images with a pinned version in their YAML (`postgresql`, `redis`/`valkey`, `alertmanager`, `grafana`, `prometheus`) are **never** affected — they always use their declared version regardless of `--tag-override`.

### 5 — Execute mirroring to an HTTP (non-TLS) registry

```bash
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry localhost:5000 \
    --execute \
    --insecure
```

### 6 — Pipe stdout directly to bash (equivalent to `--execute`)

```bash
./bin/mirror-images \
    --variant redhat-el9 \
    --dest-registry local-registry.example.com:5000 \
    | bash
```

### 7 — Inspect the generated artifact manifest

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

## Full air-gap preparation workflow

This section covers the end-to-end process of mirroring images on a connected prep machine and transferring them to an air-gapped environment.

### Step 0 — Start a local registry on the prep machine

A registry must be running at your destination address before you run `--execute`. The following uses the standard Docker registry image on port 5000.

```bash
sudo mkdir -p /opt/registry/data
```

**Option A — Port mapping (standard)**

```bash
podman run -d --name local-registry \
    -p 5000:5000 \
    -v /opt/registry/data:/var/lib/registry:z \
    --restart=always \
    docker.io/library/registry:2
```

> **Note — RHEL 9 / rootless podman:** If `curl http://localhost:5000/v2/` returns `connection refused` after starting with port mapping, firewalld or the rootless podman network stack may be blocking the forwarded port. Use Option B instead.

**Option B — Host network (RHEL 9 workaround)**

```bash
podman run -d --name local-registry \
    --network=host \
    --security-opt label=disable \
    -v /opt/registry/data:/var/lib/registry \
    --restart=always \
    docker.io/library/registry:2
```

`--network=host` bypasses podman's network stack entirely and binds directly to the host's port 5000, avoiding firewalld and rootless networking issues. The trade-off is that the container shares the host's full network namespace.

Verify the registry is ready before proceeding:

```bash
curl http://localhost:5000/v2/
# Expected: {}
```

### Step 1 — Mirror images into the registry

```bash
./bin/mirror-images \
    --variant community-el9 \
    --dest-registry localhost:5000 \
    --execute \
    --insecure
```

`--insecure` is required because `localhost:5000` serves plain HTTP. Verify all images landed:

```bash
curl http://localhost:5000/v2/_catalog
```

### Step 2 — Export the registry content for transfer

`skopeo sync --src docker` expects a repository path (`registry/image`), not a bare registry hostname. Passing `localhost:5000` alone causes skopeo to misparse it as image name `localhost` with tag `5000` from Docker Hub. The fix is to use `--src yaml`, which takes an explicit registry config file and avoids the ambiguity entirely.

First, generate the sync config dynamically from the live registry catalog:

```bash
cat << 'EOF' > /tmp/generate-sync.py
import json, urllib.request

catalog = json.loads(urllib.request.urlopen('http://localhost:5000/v2/_catalog').read())
print('localhost:5000:')
print('  tls-verify: false')
print('  images:')
for repo in catalog['repositories']:
    tags = json.loads(urllib.request.urlopen(
        f'http://localhost:5000/v2/{repo}/tags/list'
    ).read()).get('tags', [])
    if tags:
        print(f'    {repo}:')
        for tag in tags:
            print(f'      - "{tag}"')
EOF

python3 /tmp/generate-sync.py > /tmp/registry-sync.yaml
```

Then create the export directory (fixing ownership so skopeo can write as the normal user) and run the sync:

```bash
sudo mkdir -p /opt/registry/export
sudo chown $USER /opt/registry/export

skopeo sync \
    --src yaml \
    --dest dir \
    /tmp/registry-sync.yaml \
    /opt/registry/export
```

`skopeo sync --src yaml` reads the registry and image list from the YAML file rather than parsing the source as an image reference, so the `localhost:5000` hostname is resolved correctly.

Then package for transfer:

```bash
tar -czf ~/mirror-images-community-el9.tar.gz -C /opt/registry export/
```

The archive is written to your home directory (`~/`) to avoid permission issues writing into `/opt`.

### Step 3 — Transfer to the air-gapped environment

Copy the archive to the air-gapped machine using whatever transfer method is available (SCP, USB drive, shared storage):

```bash
scp ~/mirror-images-community-el9.tar.gz user@air-gapped-vm:~/
```

### Step 4 — On the air-gapped VM: import images into a local registry

Create the required directories and fix ownership before starting the registry or extracting — the registry container and skopeo both run as the normal user:

```bash
sudo mkdir -p /opt/registry/data
sudo mkdir -p /opt/registry/export
sudo chown -R $USER /opt/registry
```

Start the registry (same Option A / Option B choice as Step 0):

**Option A — Port mapping (standard)**
```bash
podman run -d --name local-registry \
    -p 5000:5000 \
    -v /opt/registry/data:/var/lib/registry:z \
    --restart=always \
    docker.io/library/registry:2
```

**Option B — Host network (RHEL 9 workaround)**
```bash
podman run -d --name local-registry \
    --network=host \
    --security-opt label=disable \
    -v /opt/registry/data:/var/lib/registry \
    --restart=always \
    docker.io/library/registry:2
```

Verify it is ready before importing:
```bash
curl http://localhost:5000/v2/
# Expected: {}
```

Then extract the archive:

```bash
tar -xzf ~/mirror-images-community-el9.tar.gz -C /opt/registry
```

`skopeo sync --src yaml` writes images into a subdirectory named after the source registry (`localhost:5000/`) inside the destination directory. When importing with `--src dir`, pass the **parent** directory — skopeo automatically descends into the `localhost:5000/` subdirectory:

```bash
skopeo sync \
    --src dir \
    --dest docker \
    --dest-tls-verify=false \
    /opt/registry/export \
    localhost:5000
```

With `--src dir` the source is an explicit filesystem path so there is no hostname parsing ambiguity, and `localhost:5000` on the destination side is treated as a plain registry address by `--dest docker`.

Verify:

```bash
curl http://localhost:5000/v2/_catalog
```

### Step 5 — Configure the system to use the local registry

Edit `/etc/containers/registries.conf` to redirect pulls from the original registries to your local mirror:

```toml
[[registry]]
prefix = "quay.io"
location = "localhost:5000"
insecure = true

[[registry]]
prefix = "docker.io"
location = "localhost:5000"
insecure = true

[[registry]]
prefix = "registry.access.redhat.com"
location = "localhost:5000"
insecure = true
```

### Step 6 — Install flightctl pointing at the local registry

```bash
helm install flightctl deploy/helm/flightctl \
    --set ubiMinimal.image=localhost:5000/ubi9/ubi-minimal \
    --set ubiMinimal.tag=9.7-1763362218 \
    ...
```

---

## Image sources

The tool reads image references from two sources per run:

| Source | Path | Content |
|--------|------|---------|
| Helm chart options | `deploy/helm/helm-chart-opts.yaml` | All FlightControl Helm component images keyed by variant, including the UBI minimal base image used by init containers (`ubiMinimal`) |
| RPM-only images (community-el9) | `packaging/images/el9/images.yaml` | Images installed by flightctl RPMs that are **not** in the Helm chart (pam-issuer, userinfo-proxy, grafana, prometheus, redis) — community registry sources |
| RPM-only images (community-el10) | `packaging/images/el10/images.yaml` | Same set of RPM-only images for the el10 community variant |
| RPM-only images (redhat-el9) | `packaging/images/rhel9/images.yaml` | Downstream `registry.redhat.io` equivalents of the RPM-only images for the redhat-el9 variant |
| RPM-only images (redhat-el10) | `packaging/images/rhel10/images.yaml` | Downstream `registry.redhat.io` equivalents of the RPM-only images for the redhat-el10 variant |

The tool selects exactly one RPM images file per run based on the variant. Images that appear in both sources with the same `image:tag` are deduplicated — each unique source reference appears exactly once in the output.

### UBI minimal base image

Several Helm-managed pods (the alertmanager-proxy init container and the `helm test` pod) use a UBI minimal base image that is not a FlightControl service image. This image is tracked in the `ubiMinimal` entry of each variant in `helm-chart-opts.yaml` so the mirror tool includes it automatically:

| Variant | Source |
|---------|--------|
| `community-el9` | `registry.access.redhat.com/ubi9/ubi-minimal` |
| `community-el10` | `registry.access.redhat.com/ubi10/ubi-minimal` |
| `redhat-el9` | `registry.redhat.io/ubi9/ubi-minimal` |
| `redhat-el10` | `registry.redhat.io/ubi10/ubi-minimal` |

When deploying air-gapped, override the image location in Helm so pods pull from your mirror instead:

```bash
helm install flightctl deploy/helm/flightctl \
    --set ubiMinimal.image=local-registry.example.com:5000/ubi9/ubi-minimal \
    --set ubiMinimal.tag=9.7-1763362218 \
    ...
```

### Tag fallback

Core FlightControl service images (`api`, `worker`, `periodic`, `pam-issuer`, `userinfo-proxy`, etc.) have no explicit `tag:` field in their source YAML files. The tool resolves an *effective tag* in this order:

1. `--tag-override <tag>` if supplied — always wins.
2. `appVersion` from `deploy/helm/flightctl/Chart.yaml` — the release version on a tagged checkout, `"latest"` on a dev branch.

Third-party images with pinned versions (`grafana`, `prometheus`, `postgresql`, `redis`, etc.) always carry an explicit tag in their YAML and are **never** affected by `--tag-override`.

**Selecting the release version:**

| Goal | How |
|------|-----|
| Mirror a specific release | `--tag-override v1.1.2` (any branch) |
| Mirror latest builds | Omit the flag on a dev branch, or `--tag-override latest` on a release branch |
| Match installed RPM version | `git checkout v1.1.2` (uses Chart.yaml) or `--tag-override v1.1.2` |

**Tag mismatch warning:** On a dev branch `appVersion` is `"latest"`, but installed RPMs are typically versioned (e.g. `v1.1.2`). The RPM quadlet files reference images by the RPM version, so a `:latest` mirror will not match. The tool prints a prominent `[WARN]` block in this case explaining the fix.

---

## Variant → registry mapping

| Variant | Source registries |
|---------|------------------|
| `community-el9` / `community-el10` | `quay.io`, `docker.io`, `registry.access.redhat.com` (UBI, no auth) |
| `redhat-el9` / `redhat-el10` | `registry.redhat.io` (requires pull secret) |

For Red Hat variants, the connected preparation system needs valid credentials for `registry.redhat.io` before running `skopeo copy`. The UBI minimal image for community variants is sourced from `registry.access.redhat.com`, which is publicly accessible and does not require authentication.

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

**`normalizeDockerImage(image)`**
Expands Docker Hub official image names to their canonical form by inserting the implicit `library/` namespace.  Docker Hub stores official images (`redis`, `nginx`, `postgres`, etc.) under the `library` organization — podman resolves `docker.io/redis` as `docker.io/library/redis` at pull time.  Without normalization, a mirror written to `registry/redis:tag` would not be found because podman looks for `registry/library/redis:tag`.  Only single-component `docker.io` paths are affected; multi-component paths (`docker.io/grafana/grafana`) and all other registries are returned unchanged.

**`ImageToDest(image, tag)`**
Strips the source registry hostname (everything up to the first `/`) and prepends `destRegistry`:

```
"quay.io/flightctl/flightctl-api-el9" + "latest"
    → "localhost:5000/flightctl/flightctl-api-el9:latest"

"docker.io/library/redis" + "7.4.1"   (after normalizeDockerImage)
    → "localhost:5000/library/redis:7.4.1"
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

# 6. All variants must include a ubi-minimal image
for v in community-el9 community-el10 redhat-el9 redhat-el10; do
    hits=$(./bin/mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null \
           | grep -c "ubi-minimal" || true)
    echo "$v: $hits ubi-minimal image (expect 1)"
done

# 7. Docker Hub official images must use the library/ namespace
hits=$(./bin/mirror-images --variant community-el9 --dest-registry localhost:5000 2>/dev/null \
       | grep -c "library/redis" || true)
echo "library/redis entries: $hits (expect 1)"

# 9. Stdout is clean — no [INFO]/[WARN] lines mixed in
dirty=$(./bin/mirror-images --variant community-el9 --dest-registry localhost:5000 2>/dev/null \
        | grep -cE "^\[INFO\]|^\[WARN\]|^\[ERROR\]" || true)
echo "Dirty stdout lines: $dirty (expect 0)"

# 10. Invalid variant exits 1
./bin/mirror-images --variant bad --dest-registry localhost:5000 2>&1; echo "exit: $?"

# 11. URL scheme rejected
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

**Init containers fail to start after air-gapped install (`ImagePullBackOff` on `init-certs` or similar)**
The alertmanager-proxy init container and the `helm test` pod use a UBI minimal base image that must be mirrored separately. Verify the image appears in the mirror output:

```bash
./bin/mirror-images --variant community-el9 --dest-registry local-registry.example.com:5000 2>/dev/null \
    | grep ubi-minimal
```

Then tell Helm to use the mirrored location at install time:

```bash
helm install flightctl deploy/helm/flightctl \
    --set ubiMinimal.image=local-registry.example.com:5000/ubi9/ubi-minimal \
    --set ubiMinimal.tag=9.7-1763362218 \
    ...
```

**`docker.io/redis` mirrored to wrong path**
Podman resolves Docker Hub official images as `docker.io/library/<name>` at pull time.  The tool automatically normalizes single-component `docker.io` paths (e.g. `docker.io/redis` → `docker.io/library/redis`) so the mirrored destination path matches what podman looks for.  If you see `registry/redis:tag` instead of `registry/library/redis:tag` in your mirror, you are running an older binary — rebuild with `make build-mirror-images`.

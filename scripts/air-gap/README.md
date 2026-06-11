# `flightctl-mirror-images` — Air-gap Image Mirroring Tool

Enumerates all FlightCtl container images for a given deployment variant and
either prints `skopeo copy` commands, executes them against a live registry, or
packages everything into a self-contained offline bundle.

For **user-facing installation documentation** see:

- [Installing the Flight Control service offline on Linux](../../docs/user/installing/installing-service-on-linux-offline.md)
- [Installing the Flight Control agent offline on RHEL](../../docs/user/installing/installing-agent-offline.md)
- [Packaging artifacts for portable media](../../docs/user/installing/offline-portable-media.md)
- [Setting up a local RPM repository](../../docs/user/installing/offline-rpm-repository.md)

---

## Quick start

```bash
# Build
make build-mirror-images

# Bundle: create a self-contained offline archive with all images + RPMs
./bin/flightctl-mirror-images --variant community-el9 \
    --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms

# Dry-run: print all skopeo commands for review
./bin/flightctl-mirror-images --variant community-el9 \
    --dest-registry local-registry.example.com:5000

# Execute: mirror images directly to a running registry
./bin/flightctl-mirror-images --variant community-el9 \
    --dest-registry local-registry.example.com:5000 \
    --execute

# Agent-only bundle (no variant required)
./bin/flightctl-mirror-images --agent-only --bundle ~/flightctl-agent-bundle.tar.gz
```

---

## Prerequisites

| Requirement | When needed |
|-------------|-------------|
| Go 1.23+ | Build (`make build-mirror-images`) |
| `skopeo` | `--execute` or `--bundle` modes |
| `dnf` + `sudo` | `--bundle-rpms` or `--agent-only` |
| `dnf-plugins-core` | `--rpm-reposync` only |
| `createrepo_c` | `--rpm-createrepo` only |

---

## Flags

### Variant (required unless `--agent-only`)

| Flag | Values |
|------|--------|
| `--variant` | `community-el9`, `community-el10`, `rhem-el9`, `rhem-el10` |

### Destination (required in non-bundle mode)

| Flag | Default | Description |
|------|---------|-------------|
| `--dest-registry` | — | Registry host:port, no scheme. Defaults to `localhost:5000` in bundle mode (used only in `import.sh`). |

### Bundle mode

| Flag | Default | Description |
|------|---------|-------------|
| `--bundle <path>` | — | Create a `.tar.gz` archive at path. Mutually exclusive with `--execute`. |
| `--bundle-rpms` | false | Add RPMs and `install-rpms.sh` to the bundle. Requires `--bundle`. |
| `--rpm-packages` | `flightctl-services,flightctl-cli,flightctl-agent` | Comma-separated packages to download. |
| `--rpm-exclude` | — | Comma-separated packages to download but exclude from auto-installation. Excluded packages remain in `rpms/` for manual use (e.g. embedding `flightctl-agent` into device OS images). |
| `--rpm-version` | — | Pin flightctl RPM packages to this exact version (e.g. `1.2.0~rc3`). When omitted and `--tag-override` is set, the version is derived automatically so RPM and image versions stay in sync. Use when the RPM version differs from the image tag. |
| `--rpm-repo-url` | `https://rpm.flightctl.io/flightctl-epel.repo` | `.repo` file URL for RPM downloads. Override to use a COPR or custom repo. |
| `--rpm-reposync` | false | Use `dnf reposync` to mirror the full repo with metadata. Mutually exclusive with `--rpm-createrepo`. |
| `--rpm-createrepo` | false | Run `createrepo_c` after `dnf download` to generate `repodata/`. Mutually exclusive with `--rpm-reposync`. |
| `--agent-only` | false | RPM-only bundle, no images, no `--variant` required. Defaults `--rpm-packages` to `flightctl-agent,flightctl-cli,open-vm-tools,ignition,afterburn,cloud-init`. |

### Available FlightCtl RPM packages

| Package | Purpose | Notes |
|---------|---------|-------|
| `flightctl-services` | Full server deployment (API, worker, DB, KV, cert generator) | Default for `--bundle-rpms` |
| `flightctl-agent` | Edge device management agent | Included in `--agent-only` defaults |
| `flightctl-cli` | `flightctl` operator CLI | Included in `--agent-only` defaults |
| `flightctl-observability` | Prometheus + Grafana observability stack | Add explicitly when needed; **Prometheus and Grafana container images are not included in the default bundle** — mirror them separately (see [Deploying Observability Offline](../../docs/user/installing/deploying-observability-linux.md#air-gapped-installation)) |
| `flightctl-selinux` | SELinux policy module | Auto-pulled as a dep of `flightctl-services` / `flightctl-agent`; no need to list explicitly |

Pass multiple packages as a comma-separated list or by repeating the flag — both forms are equivalent:

```bash
# Comma-separated
--rpm-packages flightctl-services,flightctl-observability

# Repeated flag
--rpm-packages flightctl-services --rpm-packages flightctl-observability
```

**Common combinations:**

```bash
# Server with observability RPM (Prometheus + Grafana images must be mirrored separately)
./bin/flightctl-mirror-images --variant community-el9 \
    --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms \
    --tag-override 1.2.0-rc1 \
    --rpm-packages flightctl-services,flightctl-observability

# Edge device agent + CLI only (no images)
./bin/flightctl-mirror-images \
    --agent-only \
    --bundle ~/flightctl-agent-bundle.tar.gz
```

### Live-push / dry-run mode

| Flag | Default | Description |
|------|---------|-------------|
| `--execute` | false | Run skopeo commands immediately. |
| `--insecure` | false | Add `--dest-tls-verify=false` (required for HTTP registries). |
| `--tag-override <tag>` | — | Override the image tag for untagged FlightCtl service images (e.g. `1.2.0-rc3`). Third-party images with pinned tags are unaffected. When `--bundle-rpms` is set, also pins flightctl RPM packages to the matching version (converting `-` to `~` for pre-release, e.g. `1.2.0-rc3` → `1.2.0~rc3`) unless `--rpm-packages` or `--rpm-version` is explicitly set. |

---

## Image sources

The tool reads from two sources and deduplicates:

| Source | Path | Used for |
|--------|------|----------|
| Helm chart options | `deploy/helm/helm-chart-opts.yaml` | All Helm-managed images, keyed by variant |
| Full image config | `packaging/images/{el9,el10,rhel9,rhel10}/images.yaml` | Complete image list also used by the RPM build to render quadlet files |

The `el9`/`el10` files are also consumed by `packaging/rpm/flightctl.spec` to
drive `flightctl-standalone render quadlets --config`; they must remain
complete. The `rhel9`/`rhel10` files contain only the downstream
`registry.redhat.io` equivalents for grafana and prometheus.

---

## Code layout

```
scripts/air-gap/mirror-images/
├── main.go      — CLI flags, validation, workflow orchestration
├── parser.go    — YAML parsing (helm-chart-opts, images.yaml, Chart.yaml, RPM spec)
├── mirror.go    — ImagePair type, Dedup, GenerateCommands, skopeo execution
├── manifest.go  — artifact-manifest-<variant>.yaml generation
└── bundle.go    — BundleImages, DownloadRPMs, WriteImportScript,
                   WriteInstallRPMsScript, CreateArchive
```

### Key env var overrides (for testing)

| Env var | Default path |
|---------|-------------|
| `HELM_CHART_OPTS` | `deploy/helm/helm-chart-opts.yaml` |
| `CHART_YAML` | `deploy/helm/flightctl/Chart.yaml` |
| `OBS_IMAGES_EL9` | `packaging/images/el9/images.yaml` |
| `OBS_IMAGES_EL10` | `packaging/images/el10/images.yaml` |
| `OBS_IMAGES_RHEL9` | `packaging/images/rhel9/images.yaml` |
| `OBS_IMAGES_RHEL10` | `packaging/images/rhel10/images.yaml` |
| `RPM_SPEC` | `packaging/rpm/flightctl.spec` |

---

## Testing

### Flag validation smoke tests

```bash
make build-mirror-images

# Mutually exclusive flags
./bin/flightctl-mirror-images --variant community-el9 --bundle /tmp/x.tar.gz --execute 2>&1
./bin/flightctl-mirror-images --rpm-reposync --rpm-createrepo --agent-only --bundle /tmp/x.tar.gz 2>&1
./bin/flightctl-mirror-images --agent-only --execute --bundle /tmp/x.tar.gz 2>&1
./bin/flightctl-mirror-images --agent-only 2>&1
./bin/flightctl-mirror-images --rpm-reposync --variant community-el9 --bundle /tmp/x.tar.gz 2>&1

# Invalid variant / URL scheme
./bin/flightctl-mirror-images --variant bad --dest-registry localhost:5000 2>&1
./bin/flightctl-mirror-images --variant community-el9 --dest-registry https://localhost:5000 2>&1
```

### Output correctness

```bash
# Image counts per variant
for v in community-el9 community-el10 rhem-el9 rhem-el10; do
    n=$(./bin/flightctl-mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null | wc -l)
    echo "$v: $n images"
done

# Community variants must not include registry.redhat.io
for v in community-el9 community-el10; do
    hits=$(./bin/flightctl-mirror-images --variant "$v" --dest-registry localhost:5000 2>/dev/null \
           | grep -c "registry.redhat.io" || true)
    echo "$v: $hits registry.redhat.io sources (expect 0)"
done

# Docker Hub official images must use library/ namespace
hits=$(./bin/flightctl-mirror-images --variant community-el9 --dest-registry localhost:5000 2>/dev/null \
       | grep -c "library/redis" || true)
echo "library/redis entries: $hits (expect 1)"

# stdout must be clean (no log lines)
dirty=$(./bin/flightctl-mirror-images --variant community-el9 --dest-registry localhost:5000 2>/dev/null \
        | grep -cE "^\[INFO\]|^\[WARN\]|^\[ERROR\]" || true)
echo "dirty stdout lines: $dirty (expect 0)"
```

### Fixture-based isolated test

```bash
export HELM_CHART_OPTS=scripts/air-gap/test/fixtures/helm-chart-opts.yaml
export CHART_YAML=scripts/air-gap/test/fixtures/Chart.yaml
export OBS_IMAGES_EL9=scripts/air-gap/test/fixtures/images-el9.yaml
export OBS_IMAGES_EL10=scripts/air-gap/test/fixtures/images-el10.yaml
export OBS_IMAGES_RHEL9=scripts/air-gap/test/fixtures/images-rhel9.yaml
export OBS_IMAGES_RHEL10=scripts/air-gap/test/fixtures/images-rhel10.yaml
export RPM_SPEC=/dev/null

./bin/flightctl-mirror-images --variant community-el9 --dest-registry localhost:5000
```

---

## Troubleshooting

| Error | Fix |
|-------|-----|
| `required file not found: deploy/helm/helm-chart-opts.yaml` | Run from the repo root or `git pull` |
| `invalid variant 'x'` | Use one of: `community-el9`, `community-el10`, `rhem-el9`, `rhem-el10` |
| `registry.redhat.io` auth failure | `podman login registry.redhat.io` before running |
| `appVersion is 'latest'` warning | Use `--tag-override v1.x.x` or check out a release tag |
| `N image(s) failed to copy` | Check `[ERROR]` lines on stderr; tool continues past failures |
| `ImagePullBackOff` on init containers | UBI minimal image missing from mirror — verify it appears in output with `grep ubi-minimal` |

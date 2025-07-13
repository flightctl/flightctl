# GitHub Actions Cache Mounts Configuration

This document describes the comprehensive caching strategy for GitHub Actions workflows, combining both BuildKit cache mounts and GitHub Actions native caching.

## Overview

Our caching strategy uses two complementary approaches:

1. **BuildKit Cache Mounts**: For caching within individual container builds
2. **GitHub Actions Cache**: For persisting cache between workflow runs

This dual approach ensures maximum build performance by caching:
- Go module dependencies (`/opt/app-root/src/go/pkg/mod`)
- Go build cache (`/opt/app-root/src/.cache/go-build`)
- DNF package cache (`/var/cache/dnf` and `/var/lib/dnf`)

## Cache Types Found in Container Files

After analyzing all container files in the repository, we found the following cache types:

### 1. Go Module Cache
- **Container Path**: `/opt/app-root/src/go/pkg/mod`
- **GitHub Actions Path**: `~/go/pkg/mod`
- **Purpose**: Caches downloaded Go modules to avoid re-downloading on subsequent builds

### 2. Go Build Cache
- **Container Path**: `/opt/app-root/src/.cache/go-build`
- **GitHub Actions Path**: `~/.cache/go-build`
- **Purpose**: Caches compiled Go packages to speed up incremental builds

### 3. DNF Package Cache
- **Container Paths**: `/var/cache/dnf` and `/var/lib/dnf`
- **GitHub Actions Paths**: `/var/cache/dnf` and `/var/lib/dnf`
- **Purpose**: Caches downloaded RPM packages for both `dnf` and `microdnf` package managers

## Configuration Changes

### 1. Environment Variables

All workflows that build containers include these environment variables:

```yaml
env:
  # Enable BuildKit for cache mounts
  DOCKER_BUILDKIT: 1
  BUILDKIT_PROGRESS: plain
```

### 2. GitHub Actions Native Caching

All workflows include GitHub Actions cache steps to persist all cache types:

```yaml
- name: Cache Go modules
  uses: actions/cache@v4
  with:
    path: |
      ~/.cache/go-build
      ~/go/pkg/mod
    key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
    restore-keys: |
      ${{ runner.os }}-go-

- name: Cache DNF packages
  uses: actions/cache@v4
  with:
    path: |
      /var/cache/dnf
      /var/lib/dnf
    key: ${{ runner.os }}-dnf-{workflow-specific-identifier}
    restore-keys: |
      ${{ runner.os }}-dnf-
```

**Note**: DNF package cache uses workflow-specific identifiers rather than Go dependency hashes, since package manager cache is independent of Go dependencies.

### 3. Container Build Configuration

The `publish-containers.yaml` workflow includes:

- Docker Buildx setup with BuildKit driver
- Cache mount arguments to the `redhat-actions/buildah-build@v2` action

```yaml
- name: Set up Docker Buildx
  uses: docker/setup-buildx-action@v3
  with:
    driver-opts: |
      image=moby/buildkit:v0.12.4

- name: Build
  uses: redhat-actions/buildah-build@v2
  with:
    extra-args: |
      --ulimit nofile=10000:10000
      --mount=type=cache,target=/opt/app-root/src/go/pkg/mod
      --mount=type=cache,target=/opt/app-root/src/.cache/go-build
      --mount=type=cache,target=/var/cache/dnf
      --mount=type=cache,target=/var/lib/dnf
```

## Caching Strategy

### GitHub Actions Cache (Between Workflow Runs)
- **Purpose**: Persists cache across different workflow executions
- **Scope**: Runner-level caching that survives between jobs
- **Key Strategy**: Uses `go.sum` hash to invalidate cache when dependencies change
- **Fallback**: Uses partial cache hits with `restore-keys`

### BuildKit Cache Mounts (Within Builds)
- **Purpose**: Caches within individual container build processes
- **Scope**: Build-time caching that speeds up multi-stage builds
- **Integration**: Works seamlessly with GitHub Actions cache

## Affected Workflows

The following workflows have been updated with comprehensive caching:

1. **publish-containers.yaml** - Main container publishing workflow
2. **publish-cli-bins.yaml** - CLI binary publishing workflow
3. **pr-e2e-testing.yaml** - E2E testing workflow
4. **pr-smoke-testing.yaml** - Smoke testing workflow

## Container Files Analysis

The following container files were analyzed for cache types:

- `Containerfile.api` - API server container
- `Containerfile.worker` - Worker container
- `Containerfile.periodic` - Periodic container
- `Containerfile.alert-exporter` - Alert exporter container
- `Containerfile.alertmanager-proxy` - Alertmanager proxy container
- `Containerfile.userinfo-proxy` - User info proxy container
- `Containerfile.cli-artifacts` - CLI artifacts container
- `Containerfile.db-setup` - Database setup container

All container files use the same three cache types, ensuring consistency across the build pipeline.

## Cache Types and Locations

### Go Module Cache
- **GitHub Actions**: `~/go/pkg/mod`
- **BuildKit**: `/opt/app-root/src/go/pkg/mod`
- **Purpose**: Caches downloaded Go modules

### Go Build Cache
- **GitHub Actions**: `~/.cache/go-build`
- **BuildKit**: `/opt/app-root/src/.cache/go-build`
- **Purpose**: Caches compiled Go packages

### DNF Package Cache
- **GitHub Actions**: `/var/cache/dnf` and `/var/lib/dnf`
- **BuildKit**: `/var/cache/dnf` and `/var/lib/dnf`
- **Purpose**: Caches downloaded RPM packages for both `dnf` and `microdnf`

## Benefits

1. **Faster First Builds**: GitHub Actions cache provides pre-warmed dependencies
2. **Faster Subsequent Builds**: Both cache layers work together for maximum speed
3. **Reduced Network Usage**: Less bandwidth consumption from re-downloading
4. **Cost Savings**: Significantly reduced GitHub Actions minutes
5. **Consistent Performance**: Predictable build times across runs
6. **Package Manager Efficiency**: DNF cache reduces package download time

## Cache Invalidation

- **Go Modules**: Invalidated when `go.sum` changes
- **Build Cache**: Automatically managed by Go build system
- **Package Cache**: Managed by BuildKit and uses workflow-specific cache keys

## Verification

To verify that caching is working:

1. **GitHub Actions Cache**: Check workflow logs for cache hit/miss messages
2. **BuildKit Cache**: Look for BuildKit cache mount messages in build logs
3. **Performance**: Compare build times between first and subsequent runs
4. **Cache Size**: Monitor cache usage in GitHub Actions settings
5. **DNF Cache**: Check for reduced package download times

## Troubleshooting

### GitHub Actions Cache Issues
1. Check cache key strategy and ensure `go.sum` is being tracked
2. Verify cache paths are correct for the runner OS
3. Check GitHub Actions cache storage limits
4. Ensure DNF cache paths are accessible

### BuildKit Cache Issues
1. Ensure `DOCKER_BUILDKIT=1` is set in workflow environment
2. Verify BuildKit driver is properly configured
3. Check that cache mount arguments are correctly passed

### General Issues
1. Monitor disk space on runners
2. Check for cache corruption (clear cache if needed)
3. Verify cache permissions and access
4. Ensure DNF package cache directories exist

## Performance Monitoring

Track these metrics to ensure caching effectiveness:

- **Cache Hit Rate**: Percentage of successful cache retrievals
- **Build Time Reduction**: Time saved compared to uncached builds
- **Cache Size**: Monitor storage usage and cleanup needs
- **Network Usage**: Reduction in dependency and package downloads
- **DNF Package Downloads**: Reduction in RPM package downloads

## Future Enhancements

- Consider implementing cache warming strategies for critical build paths
- Monitor and optimize cache key strategies based on usage patterns
- Explore additional cache targets for other build dependencies
- Implement cache analytics and reporting
- Consider caching other package manager caches if new ones are added 
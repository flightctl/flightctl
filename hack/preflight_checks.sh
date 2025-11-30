#!/usr/bin/env bash
set -euo pipefail

# Container-side preflight checks for RPM build environment

ROOT="${1:-}"  # Mock root name (optional)
HAS_WARNINGS=false

print_warning() {
  local msg="$1"
  HAS_WARNINGS=true
  printf 'WARNING:\t%s\n' "$msg"
  # GitHub Actions warning format (only when running in GitHub Actions)
  if [[ "${GITHUB_ACTIONS:-false}" == "true" ]]; then
    printf '::warning title=Packit RPM Builder Preflight Check::%s\n' "$msg"
  fi
}

print_ok() {
  local msg="$1"
  printf 'OK:\t%s\n' "$msg"
}

# Check timestamp file and report age
# Usage: check_timestamp_age "file_path" "description" "rebuild_command"
check_timestamp_age() {
  local timestamp_file="$1"
  local description="$2"
  local rebuild_cmd="$3"

  if [[ ! -f "$timestamp_file" ]]; then
    print_warning "No timestamp found for ${description}"
    return
  fi

  local created_str
  created_str="$(cat "$timestamp_file" 2>/dev/null || true)"
  if [[ -z "$created_str" ]]; then
    print_warning "Could not read timestamp for ${description}"
    return
  fi

  local created_epoch
  created_epoch="$(date -d "$created_str" +%s 2>/dev/null || true)"
  if [[ -z "$created_epoch" ]]; then
    print_warning "Could not parse timestamp for ${description}: ${created_str}"
    return
  fi

  local now_epoch age_seconds age_days two_weeks_seconds
  now_epoch="$(date +%s)"
  age_seconds=$((now_epoch - created_epoch))
  age_days=$((age_seconds / 86400))
  two_weeks_seconds=$((14 * 86400))

  if [[ $age_seconds -gt $two_weeks_seconds ]]; then
    print_warning "${description} is ${age_days} days old (>14 days) - consider rebuilding with ${rebuild_cmd}"
  else
    print_ok "${description} is ${age_days} days old"
  fi
}

echo "=== preflight checks ==="

# Go module cache
mod_cache="${GOMODCACHE:-/root/go/pkg/mod}"
if [[ ! -d "${mod_cache}" ]] || [[ -z "$(ls -A "${mod_cache}" 2>/dev/null)" ]]; then
  print_warning "Go module cache (${mod_cache}) is empty - build will be slower. Ensure proper mounting of the host's Go module cache directory."
else
  print_ok "Go module cache (${mod_cache}) has content"
fi

# Go build cache
build_cache="${GOCACHE:-/root/.cache/go-build}"
if [[ ! -d "${build_cache}" ]] || [[ -z "$(ls -A "${build_cache}" 2>/dev/null)" ]]; then
  print_warning "Go build cache (${build_cache}) is empty - build will be slower. Ensure proper mounting of the host's Go build cache directory."
else
  print_ok "Go build cache (${build_cache}) has content"
fi

# Mock configuration
if [[ -f /etc/mock/site-defaults.cfg ]]; then
  print_ok "Mock site defaults file found at /etc/mock/site-defaults.cfg"
else
  print_warning "Mock site defaults file not found at /etc/mock/site-defaults.cfg"
fi

# Container image age (timestamp files)
found_timestamps=false
for timestamp_file in /etc/rpm-builder-image-timestamp-*; do
  if [[ -f "$timestamp_file" ]]; then
    found_timestamps=true
    filename="$(basename "$timestamp_file")"

    if [[ "$filename" == *"-base" ]]; then
      check_timestamp_age "$timestamp_file" "Base image" "--rebuild-image"
    elif [[ "$filename" == *"-cache-"* ]]; then
      cache_name="${filename#*-cache-}"
      check_timestamp_age "$timestamp_file" "Cache image '${cache_name}'" "--rebuild-image --root <root_name>"
    fi
  fi
done

if [[ "${found_timestamps}" == false ]]; then
  print_warning "No image timestamp files found under /etc/rpm-builder-image-timestamp-* - this may be an older image"
fi

# Mock root cache status (if root provided)
if [[ -n "$ROOT" ]]; then
  # Use glob pattern to find cache directories that contain the root name
  cache_dirs=(/var/cache/mock/*"${ROOT}"*/root_cache)

  found_cache=false
  for cache_dir in "${cache_dirs[@]}"; do
    if [[ -d "$cache_dir" ]]; then
      found_cache=true
      print_ok "Mock root cache directory exists for ${ROOT}: ${cache_dir}"

      if find "$cache_dir" -mindepth 1 -print -quit 2>/dev/null | grep -q .; then
        print_ok "Mock root cache for ${ROOT} has content"
      else
        print_warning "Mock root cache for ${ROOT} is empty - first build will be slower"
      fi
      break  # Found one, that's enough
    fi
  done

  if [[ "$found_cache" == false ]]; then
    print_warning "No mock root cache directory found for ${ROOT} in /var/cache/mock/ - first build will be slower"
  fi
fi

if [[ "${HAS_WARNINGS}" == true ]]; then
  echo "=== preflight checks completed with WARNINGS ==="
else
  echo "=== preflight checks completed successfully ==="
fi

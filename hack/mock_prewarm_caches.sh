#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 MOCK_ROOT [MOCK_ROOT...]" >&2
  exit 1
fi

# Sanitizes a mock root name for safe use in filenames
sanitize_filename() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9_.-' '_'
}

for r in "$@"; do
  echo "Initializing mock root $r"
  # Create chroot and caches (root cache, package manager cache etc)
  mock -r "$r" --enable-network --init

  # Resolve the toolchain constraint before publishing a reusable builder.
  mock -r "$r" --install '(golang >= 1.27.3 with golang < 1.28)'

  # Drop only the live chroot, keep caches to make runtime builds fast
  mock -r "$r" --scrub=chroot

  # Store timestamp for this specific mock root cache with sanitized filename
  sanitized_root=$(sanitize_filename "$r")
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "/etc/rpm-builder-image-timestamp-cache-${sanitized_root}"
  echo "Cached mock root $r completed at $(date)"
done

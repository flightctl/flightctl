#!/usr/bin/env bash

if [[ -n "$(git status -s)" ]]; then
  # See https://packit.dev/source-git/work-with-source-git/build-locally
  echo "WARNING: There are uncommitted git changes. This RPM build will NOT include them."
  echo "         Please commit or stash them first."
  sleep 1
fi

set -ex

CI_RPM_IMAGE=${CI_RPM_IMAGE:-quay.io/flightctl/ci-rpm-builder:latest}

# if FLIGHTCTL_RPM is set, exit
if [ -n "${FLIGHTCTL_RPM:-}" ]; then
    echo "Skipping rpm build, as FLIGHTCTL_RPM is set to ${FLIGHTCTL_RPM}"
    rm bin/rpm/* 2>/dev/null || true
    exit 0
fi

# Given that the SELinux policies are so sensitive to versioning issues, make sure to always build
# the RPM in a known environment.
echo "Building RPMs in container"

mkdir -p bin
cat >bin/build_rpms.sh <<'EOF'
set -ex
/work/hack/build_rpms_packit.sh
EOF

podman pull "${CI_RPM_IMAGE}"
# Reuse Go build/module caches from the host to speed up builds
HOST_GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"
HOST_GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
mkdir -p "${HOST_GOMODCACHE}" "${HOST_GOCACHE}"

podman run --privileged --rm -t \
  -v "$(pwd)":/work \
  -v "${HOST_GOMODCACHE}":/root/go/pkg/mod \
  -v "${HOST_GOCACHE}":/root/.cache/go-build \
  -e GOPATH=/root/go \
  -e GOMODCACHE=/root/go/pkg/mod \
  -e GOCACHE=/root/.cache/go-build \
  "${CI_RPM_IMAGE}" bash /work/bin/build_rpms.sh

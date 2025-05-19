#!/usr/bin/env bash
set -ex

# This only works on rpm based systems, for non-rpm this is wrapped by build_rpms.sh
packit 2>/dev/null >/dev/null || (echo "Installing packit" && sudo dnf install -y packit)

# Remove existing artifacts from the previous build
rm -f "$(uname -m)"/flightctl-*.rpm 2>/dev/null || true
rm -f bin/rpm/* 2>/dev/null || true
mkdir -p bin/rpm

# Save the spec as packit will modify it locally to inject versioning and we don't want that
cp packaging/rpm/flightctl.spec /tmp
# Always restore the original .spec file on exit
trap 'cp /tmp/flightctl.spec packaging/rpm/flightctl.spec || true' EXIT

packit build locally

mv noarch/flightctl-*.rpm bin/rpm
mv $(uname -m)/flightctl-*.rpm bin/rpm

# Remove artifacts left in the spec directory
rm -f packaging/rpm/*.tar.gz || true
rm -rf packaging/rpm/flightctl-*-build/ || true

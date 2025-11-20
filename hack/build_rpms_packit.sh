#!/usr/bin/env bash
set -ex

ROOT=""
PACKIT_OUTPUT_DIR="$(uname -m)"
TAIL_PID=""

if [[ "${1-}" == "--root" && -n "${2-}" ]]; then
  ROOT="$2"
  PACKIT_OUTPUT_DIR="mock-${ROOT}"
  shift 2
fi

cleanup() {
  # Restore original spec
  cp /tmp/flightctl.spec packaging/rpm/flightctl.spec || true
  # Stop tail if it is running
  if [[ -n "${TAIL_PID:-}" ]]; then
    kill "$TAIL_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# This only works on rpm based systems, for non-rpm this is wrapped by build_rpms.sh
packit 2>/dev/null >/dev/null || (echo "Installing packit" && dnf install -y packit)

# Remove existing artifacts from the previous build
rm -f "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm 2>/dev/null || true
rm -f bin/rpm/* 2>/dev/null || true
mkdir -p bin/rpm

# Save the spec as packit will modify it locally to inject versioning and we don't want that
cp packaging/rpm/flightctl.spec /tmp

echo "Git metadata: TAG=${SOURCE_GIT_TAG:-unset}, COMMIT=${SOURCE_GIT_COMMIT:-unset}, TREE_STATE=${SOURCE_GIT_TREE_STATE:-unset}, TAG_NO_V=${SOURCE_GIT_TAG_NO_V:-unset}, BIN_TIMESTAMP=${BIN_TIMESTAMP:-unset}"


if [[ -n "$ROOT" ]]; then
  # Prepare result dir and log file for mock
  mkdir -p "$PACKIT_OUTPUT_DIR"
  : > "$PACKIT_OUTPUT_DIR/build.log"

  echo "Starting packit build in-mock (root=$ROOT), resultdir=$PACKIT_OUTPUT_DIR"
  echo "List of supported systems:"
  mock --list-chroots
  # Run packit in background so we can tail the log
  packit build in-mock --root "$ROOT" --resultdir "$PACKIT_OUTPUT_DIR" &
  PACKIT_PID=$!

  echo "Tailing $PACKIT_OUTPUT_DIR/build.log (mock build log)..."
  tail -F "$PACKIT_OUTPUT_DIR/build.log" &
  TAIL_PID=$!

  # Wait for packit / mock to finish, then handle exit code
  set +e
  wait "$PACKIT_PID"
  BUILD_RC=$?
  set -e

  if [[ $BUILD_RC -ne 0 ]]; then
    echo "packit build in-mock failed with exit code $BUILD_RC" >&2
    exit "$BUILD_RC"
  fi
else
  # Local build: output goes directly to stdout/stderr
  echo "Starting local packit build"
  packit build locally
fi

# Move resulting RPMs
mv "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm bin/rpm
mv noarch/flightctl-*.rpm bin/rpm || true

# Remove artifacts left in the spec directory
rm -f packaging/rpm/*.tar.gz || true
rm -rf packaging/rpm/flightctl-*-build/ || true

#!/usr/bin/env bash
set -e

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


install_packit() {
  packit >/dev/null 2>&1 || {
    echo "Installing packit"
    dnf install -y packit
  }
}

prepare_workspace() {
  # Remove existing artifacts from the previous build
  rm -f "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm 2>/dev/null || true
  rm -f bin/rpm/* 2>/dev/null || true
  mkdir -p bin/rpm

  # Save the spec as packit will modify it locally to inject versioning
  cp packaging/rpm/flightctl.spec /tmp
}

run_mock_build() {
  mkdir -p "$PACKIT_OUTPUT_DIR"
  : > "$PACKIT_OUTPUT_DIR/build.log"

  echo "Starting packit build in-mock (root=$ROOT), resultdir=$PACKIT_OUTPUT_DIR"

  # Run packit in background so we can tail the log
  packit -d build in-mock --root "$ROOT" --resultdir "$PACKIT_OUTPUT_DIR" &
  local packit_pid=$!

  echo "Tailing $PACKIT_OUTPUT_DIR/build.log (mock build log)..."
  tail -F "$PACKIT_OUTPUT_DIR/build.log" &
  TAIL_PID=$!

  # Wait for packit / mock to finish, then handle exit code
  set +e
  wait "$packit_pid"
  local build_rc=$?
  set -e

  if [[ $build_rc -ne 0 ]]; then
    echo "packit build in-mock failed with exit code $build_rc" >&2
    exit "$build_rc"
  fi
}

run_local_build() {
  echo "Starting local packit build with debug logging"
  packit -d build locally
}

move_artifacts() {
  # Verify at least one RPM was created
  if ! ls "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm 1>/dev/null 2>&1; then
    echo "Error: No RPMs found in $PACKIT_OUTPUT_DIR" >&2
    exit 1
  fi

  mv "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm bin/rpm
  mv noarch/flightctl-*.rpm bin/rpm || true
}

cleanup_packaging_artifacts() {
  rm -f packaging/rpm/*.tar.gz || true
  rm -rf packaging/rpm/flightctl-*-build/ || true
}

./hack/preflight_checks.sh "${ROOT}"

echo "::group::Preparing RPM build environment"
install_packit
prepare_workspace
echo "::endgroup::"

if [[ -n "$ROOT" ]]; then
  echo "::group::Building RPM in $ROOT"
  run_mock_build
else
  echo "::group::Building RPM locally"
  run_local_build
fi
echo "::endgroup::"

move_artifacts
cleanup_packaging_artifacts

echo "Build completed successfully"

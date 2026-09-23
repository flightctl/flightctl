#!/usr/bin/env bash
set -e

ROOT=""
PACKIT_OUTPUT_DIR="$(uname -m)"
TAIL_PID=""
SPEC_PATH="packaging/rpm/flightctl.spec"
# Populated from the spec after prepare_workspace (see load_expected_rpm_packages).
EXPECTED_RPM_PACKAGES=()

if [[ "${1-}" == "--root" && -n "${2-}" ]]; then
  ROOT="$2"
  PACKIT_OUTPUT_DIR="mock-${ROOT}"
  shift 2
fi

cleanup() {
  # Restore original spec
  cp /tmp/flightctl.spec "$SPEC_PATH" || true

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
  rm -f noarch/flightctl-*.rpm 2>/dev/null || true
  rm -f bin/rpm/* 2>/dev/null || true
  mkdir -p bin/rpm

  # Save the spec as packit will modify it locally to inject versioning
  cp "$SPEC_PATH" /tmp/flightctl.spec
}

# Binary RPMs come from %package entries. The main Name: package has no %files
# and is not built; .src.rpm is not required by consumers of bin/rpm/.
load_expected_rpm_packages() {
  local spec="$1"
  local name subpkg
  name="$(awk '/^Name:/{print $2; exit}' "$spec")"
  if [[ -z "$name" ]]; then
    echo "Error: could not read Name from ${spec}" >&2
    exit 1
  fi

  EXPECTED_RPM_PACKAGES=()
  while read -r subpkg; do
    [[ -n "$subpkg" ]] || continue
    EXPECTED_RPM_PACKAGES+=("${name}-${subpkg}")
  done < <(awk '/^%package[[:space:]]+/ {print $2}' "$spec")

  if ((${#EXPECTED_RPM_PACKAGES[@]} == 0)); then
    echo "Error: no %package entries found in ${spec}" >&2
    exit 1
  fi
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

  return "$build_rc"
}

rpm_exists() {
  local pkg="$1"
  ls "$PACKIT_OUTPUT_DIR"/"${pkg}"-*.rpm >/dev/null 2>&1 \
    || ls noarch/"${pkg}"-*.rpm >/dev/null 2>&1
}

missing_rpms() {
  local pkg
  local missing=()
  for pkg in "${EXPECTED_RPM_PACKAGES[@]}"; do
    if ! rpm_exists "$pkg"; then
      missing+=("$pkg")
    fi
  done
  printf '%s\n' "${missing[@]}"
}

artifacts_complete() {
  local missing
  missing="$(missing_rpms)"
  [[ -z "$missing" ]]
}

run_local_build() {
  echo "Starting local packit build with debug logging"
  packit -d build locally
}

move_artifacts() {
  if ! artifacts_complete; then
    echo "Error: Incomplete RPM set in ${PACKIT_OUTPUT_DIR} or noarch/" >&2
    local missing
    missing="$(missing_rpms | tr '\n' ' ')"
    echo "Missing expected packages: ${missing}" >&2
    exit 1
  fi

  if ls "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm >/dev/null 2>&1; then
    mv "$PACKIT_OUTPUT_DIR"/flightctl-*.rpm bin/rpm
  fi
  if ls noarch/flightctl-*.rpm >/dev/null 2>&1; then
    mv noarch/flightctl-*.rpm bin/rpm
  fi
}

cleanup_packaging_artifacts() {
  rm -f packaging/rpm/*.tar.gz || true
  rm -rf packaging/rpm/flightctl-*-build/ || true
  rm -f flightctl-*.src.rpm || true
  rm -rf "$PACKIT_OUTPUT_DIR" || true
}

./hack/preflight_checks.sh "${ROOT}"

echo "::group::Preparing RPM build environment"
install_packit
prepare_workspace
# Use the saved pre-packit spec so package discovery is stable.
load_expected_rpm_packages /tmp/flightctl.spec
echo "Expecting RPM packages: ${EXPECTED_RPM_PACKAGES[*]}"
echo "::endgroup::"

BUILD_RC=0
if [[ -n "$ROOT" ]]; then
  echo "::group::Building RPM in $ROOT"
  run_mock_build || BUILD_RC=$?
else
  echo "::group::Building RPM locally"
  run_local_build || BUILD_RC=$?
fi
echo "::endgroup::"

if artifacts_complete; then
  move_artifacts
  cleanup_packaging_artifacts
  if [[ "$BUILD_RC" -ne 0 ]]; then
    echo "WARNING: packit exited with code ${BUILD_RC} (often mock chroot cleanup after a successful build), but the full RPM set was installed to bin/rpm/" >&2
  fi
  echo "Build completed successfully"
  exit 0
fi

MISSING="$(missing_rpms | tr '\n' ' ')"
if [[ "$BUILD_RC" -ne 0 ]]; then
  echo "packit build failed with exit code ${BUILD_RC}; incomplete RPM set in ${PACKIT_OUTPUT_DIR} (missing: ${MISSING})" >&2
  exit "$BUILD_RC"
fi

echo "Error: Incomplete RPM set in ${PACKIT_OUTPUT_DIR} (missing: ${MISSING})" >&2
exit 1

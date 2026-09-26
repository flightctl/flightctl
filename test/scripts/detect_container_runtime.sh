#!/usr/bin/env bash
set -euo pipefail

# Keep the socket precedence here aligned with
# test/harness/containers/docker_host.go (detectContainerSocket,
# ConfigureDockerHost, RuntimeCLIName).

detect_container_socket() {
  local uid home_dir

  uid="$(id -u)"
  home_dir=""

  if command -v getent >/dev/null 2>&1; then
    home_dir="$(getent passwd "${uid}" | cut -d: -f6 2>/dev/null || true)"
  fi
  if [[ -z "${home_dir}" ]]; then
    home_dir="${HOME:-}"
  fi

  if [[ -n "${XDG_RUNTIME_DIR:-}" ]]; then
    if [[ -S "${XDG_RUNTIME_DIR}/podman/podman.sock" ]]; then
      printf '%s\n' "${XDG_RUNTIME_DIR}/podman/podman.sock"
      return 0
    fi
  fi

  if [[ "${uid}" != "0" ]]; then
    if [[ -S "/run/user/${uid}/podman/podman.sock" ]]; then
      printf '%s\n' "/run/user/${uid}/podman/podman.sock"
      return 0
    fi
  fi

  if [[ -n "${home_dir}" ]]; then
    if [[ -S "${home_dir}/.local/share/containers/podman/machine/podman.sock" ]]; then
      printf '%s\n' "${home_dir}/.local/share/containers/podman/machine/podman.sock"
      return 0
    fi
  fi

  if [[ -S /var/run/docker.sock ]]; then
    printf '%s\n' /var/run/docker.sock
    return 0
  fi

  if [[ -S /run/podman/podman.sock ]]; then
    printf '%s\n' /run/podman/podman.sock
    return 0
  fi

  return 1
}

configure_testcontainers_docker_host() {
  local socket_path

  if [[ -n "${DOCKER_HOST:-}" ]]; then
    return 0
  fi

  if socket_path="$(detect_container_socket)"; then
    export DOCKER_HOST="unix://${socket_path}"
  fi
}

detect_testcontainers_runtime() {
  if [[ "${DOCKER_HOST:-}" == *podman* ]]; then
    printf '%s\n' podman
    return 0
  fi

  if [[ -n "${DOCKER_HOST:-}" ]]; then
    printf '%s\n' docker
    return 0
  fi

  printf '%s\n' docker
}

TESTCONTAINERS_PODMAN_SERVICE_PID=""
TESTCONTAINERS_PODMAN_SERVICE_DIR=""

# OCI package-mode images need Podman's API so the shared E2E runner can load
# them without changing their manifest digest.
testcontainers_podman_api_ready() {
  local socket_path="$1"
  [[ -S "${socket_path}" ]] || return 1
  podman --url "unix://${socket_path}" info >/dev/null 2>&1
}

stop_testcontainers_podman_service() {
  if [[ -n "${TESTCONTAINERS_PODMAN_SERVICE_PID}" ]]; then
    kill "${TESTCONTAINERS_PODMAN_SERVICE_PID}" 2>/dev/null || true
    wait "${TESTCONTAINERS_PODMAN_SERVICE_PID}" 2>/dev/null || true
    TESTCONTAINERS_PODMAN_SERVICE_PID=""
  fi

  if [[ -n "${TESTCONTAINERS_PODMAN_SERVICE_DIR}" ]]; then
    rm -rf -- "${TESTCONTAINERS_PODMAN_SERVICE_DIR}" || true
    TESTCONTAINERS_PODMAN_SERVICE_DIR=""
  fi
}

ensure_testcontainers_podman_runtime() {
  local socket_path log_path runtime

  configure_testcontainers_docker_host
  runtime="$(detect_testcontainers_runtime)"
  if [[ "${runtime}" == "podman" && -n "${DOCKER_HOST:-}" && "${DOCKER_HOST}" != unix://* ]]; then
    export CONTAINER_HOST="${DOCKER_HOST}"
    return 0
  fi
  if [[ "${runtime}" == "podman" && "${DOCKER_HOST:-}" == unix://* ]]; then
    socket_path="${DOCKER_HOST#unix://}"
    if testcontainers_podman_api_ready "${socket_path}"; then
      export CONTAINER_HOST="${DOCKER_HOST}"
      return 0
    fi
  fi

  if ! command -v podman >/dev/null 2>&1; then
    echo "ERROR: package-mode OCI image staging requires Podman to preserve the manifest digest"
    return 1
  fi

  if ! TESTCONTAINERS_PODMAN_SERVICE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flightctl-e2e-podman.XXXXXX")"; then
    echo "ERROR: failed to create a temporary directory for the Podman API socket"
    return 1
  fi
  socket_path="${TESTCONTAINERS_PODMAN_SERVICE_DIR}/podman.sock"
  log_path="${TESTCONTAINERS_PODMAN_SERVICE_DIR}/service.log"
  env -u DOCKER_HOST -u CONTAINER_HOST podman system service --time=0 \
    "unix://${socket_path}" >"${log_path}" 2>&1 &
  TESTCONTAINERS_PODMAN_SERVICE_PID=$!
  export DOCKER_HOST="unix://${socket_path}"
  export CONTAINER_HOST="${DOCKER_HOST}"

  for _ in {1..30}; do
    if testcontainers_podman_api_ready "${socket_path}"; then
      echo "Started temporary Podman API service for E2E tests"
      return 0
    fi
    if ! kill -0 "${TESTCONTAINERS_PODMAN_SERVICE_PID}" 2>/dev/null; then
      break
    fi
    sleep 1
  done

  echo "ERROR: Podman API did not become available at ${socket_path}"
  cat "${log_path}" 2>/dev/null || true
  stop_testcontainers_podman_service
  return 1
}

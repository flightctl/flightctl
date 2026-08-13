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

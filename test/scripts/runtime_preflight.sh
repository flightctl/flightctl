#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "Usage: $0 {kind|e2e-prepare|e2e-run|quadlets|clean}" >&2
    exit 2
}

[[ $# -eq 1 ]] || usage
mode="$1"
case "${mode}" in
    kind|e2e-prepare|e2e-run|quadlets|clean) ;;
    *) usage ;;
esac

# Root invocation already selects the rootful Podman and systemd contexts. The
# checks below describe only the extra host setup needed by an unprivileged user.
if [[ "$(id -u)" -eq 0 ]]; then
    exit 0
fi

fail() {
    echo "Rootless preflight failed: $*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "'$1' is required; install it and retry."
}

check_user_systemd() {
    require_command systemctl
    if ! systemctl --user show-environment >/dev/null 2>&1; then
        fail "the user systemd manager is unavailable. Start a user session; if services must survive logout, ask an administrator to enable lingering for ${USER:-$(id -un)}."
    fi
}

check_user_systemd_kvm_access() {
    if ! systemd-run --user --wait --quiet /usr/bin/test -r /dev/kvm -a -w /dev/kvm >/dev/null 2>&1; then
        fail "the user systemd manager cannot access /dev/kvm with its current group set. If this user was recently added to the KVM group, restart the user manager (for example, log out and back in or ask an administrator to terminate the lingering user session) before deploying Quadlets."
    fi
}

check_podman() {
    require_command podman
    if [[ -n "${CONTAINER_HOST:-}" || -n "${CONTAINER_CONNECTION:-}" ]]; then
        fail "rootless Make targets require the local Podman store. Unset CONTAINER_HOST and CONTAINER_CONNECTION, then retry."
    fi
    local podman_context
    podman_context="$(podman info --format '{{.Host.Security.Rootless}} {{.Host.ServiceIsRemote}}' 2>/dev/null || true)"
    [[ "${podman_context}" == "true false" ]] || fail "Podman must use this user's local rootless service. Check Podman setup, unset remote connection settings, and verify subordinate UID/GID ranges in /etc/subuid and /etc/subgid with 'podman info'."

    local username
    username="$(id -un)"
    if [[ ! -r /etc/subuid ]] || ! grep -qE "^(${username}|$(id -u)):[0-9]+:[1-9][0-9]*$" /etc/subuid; then
        fail "no subordinate UID range is configured for ${username} in /etc/subuid. Ask an administrator to configure subordinate IDs for rootless Podman."
    fi
    if [[ ! -r /etc/subgid ]] || ! grep -qE "^(${username}|$(id -g)):[0-9]+:[1-9][0-9]*$" /etc/subgid; then
        fail "no subordinate GID range is configured for ${username} in /etc/subgid. Ask an administrator to configure subordinate IDs for rootless Podman."
    fi

}

check_crun() {
    local oci_runtime
    oci_runtime="$(podman info --format '{{.Host.OCIRuntime.Name}}' 2>/dev/null || true)"
    [[ "${oci_runtime}" == "crun" ]] || fail "rootless ${mode} needs Podman's keep-groups option to preserve supplementary groups, which requires crun (current OCI runtime: ${oci_runtime:-unknown}). Configure Podman to use crun and retry."
}

check_cgroup_v2() {
    local fs_type
    fs_type="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || true)"
    [[ "${fs_type}" == "cgroup2fs" ]] || fail "rootless Kind and Quadlet workers require a unified cgroup v2 hierarchy (/sys/fs/cgroup is ${fs_type:-unavailable})."
}

check_kvm() {
    local reason
    case "${mode}" in
        e2e-run)
            reason="E2E VM execution requires /dev/kvm"
            ;;
        e2e-prepare)
            reason="rootless bootc QCOW preparation uses native image-builder --in-vm and requires /dev/kvm"
            ;;
        kind|quadlets)
            reason="rootless ImageExport uses native image-builder --in-vm and requires /dev/kvm"
            ;;
        *)
            reason="${mode} requires /dev/kvm"
            ;;
    esac
    [[ -c /dev/kvm && -r /dev/kvm && -w /dev/kvm ]] || fail "${reason} with read/write access. Grant access through the KVM group or a per-user device ACL. Refresh the login session only when changing group membership; applying a device ACL may require administrator or udev configuration."
    if [[ "${mode}" == "kind" ]]; then
        echo "Rootless Kind's ImageBuilder worker is nested inside the node; a host per-user /dev/kvm ACL with effective read/write permissions may be needed because the KVM group may not map into the worker. The ACL only grants device-file DAC access, can be lost when the device is recreated, and cannot bypass SELinux or device-cgroup restrictions. The worker checks access when ImageExport starts." >&2
    fi
}

check_artifact_ownership() {
    local path="$1" uid
    uid="$(id -u)"
    [[ -e "${path}" ]] || return 0
    local other_owner
    if ! other_owner="$(find "${path}" -xdev ! -uid "${uid}" -print -quit 2>/dev/null)"; then
        fail "cannot inspect ownership under '${path}'. Check its permissions or clean it as the owning user before retrying."
    fi
    if [[ -n "${other_owner}" ]]; then
        fail "Rootless preparation found '${path}' with files not owned by uid ${uid}. Clean it as the owning user before retrying; this preflight will not change ownership or delete files."
    fi
}

check_repo_bin_writable() {
    local repo_root bin_dir
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    bin_dir="${repo_root}/bin"
    if [[ -e "${bin_dir}" ]]; then
        [[ -d "${bin_dir}" && -w "${bin_dir}" ]] || fail "the checkout's '${bin_dir}' build-output directory is not writable by uid $(id -u). If root created it, run 'make clean-all' as root or make the directory writable for this user."
    else
        [[ -w "${repo_root}" ]] || fail "the checkout '${repo_root}' is not writable by uid $(id -u), so Make cannot create its bin/ build-output directory."
    fi
}

check_e2e_artifact_ownership() {
    local repo_root path
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    local -a paths=(
        "${repo_root}/bin/.rpm"
        "${repo_root}/bin/rpm"
        "${repo_root}/bin/brew-rpm"
        "${repo_root}/bin/.e2e-agent-certs"
        "${repo_root}/bin/.e2e-agent-injected"
        "${repo_root}/bin/e2e-certs"
        "${repo_root}/bin/.ssh"
        "${repo_root}/bin/agent-artifacts"
        "${repo_root}/bin/app-images-bundle.tar"
        "${repo_root}/bin/output/qcow2"
        "${repo_root}/bin/rootless-bib-cache"
    )
    shopt -s nullglob
    paths+=("${repo_root}"/bin/.e2e-agent-images-*)
    paths+=("${repo_root}"/bin/output/agent-qcow2-*)
    shopt -u nullglob

    for path in "${paths[@]}"; do
        check_artifact_ownership "${path}"
    done
}

check_mock_artifact_ownership() {
    local repo_root mock_root path uid other_owner other_owner_uid
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    mock_root="${RPM_MOCK_ROOT:-centos-stream+epel-next-9-x86_64}"
    path="${repo_root}/mock-${mock_root}"
    [[ -e "${path}" ]] || return 0

    uid="$(id -u)"
    if ! other_owner="$(find "${path}" -xdev ! -uid "${uid}" -print -quit 2>/dev/null)"; then
        other_owner_uid="$(stat -c '%u' -- "${path}" 2>/dev/null || true)"
        fail "cannot inspect Mock RPM output '${path}' (top-level owner uid ${other_owner_uid:-unknown}). A failed rootful Packit build can leave uid-0 output behind. Run 'make clean-e2e-agent-images RPM_MOCK_ROOT=${mock_root}' as the owning UID; cleanup leaves paths it cannot inspect untouched."
    fi
    if [[ -n "${other_owner}" ]]; then
        other_owner_uid="$(stat -c '%u' -- "${other_owner}" 2>/dev/null || true)"
        fail "Rootless E2E preparation found Mock RPM output '${path}' with entries not owned by uid ${uid}; for example, '${other_owner}' is owned by uid ${other_owner_uid:-unknown}. A failed rootful Packit build can leave uid-0 output behind. Run 'make clean-e2e-agent-images RPM_MOCK_ROOT=${mock_root}' as the owning UID when the tree has a single owner. If ownership is mixed, clean each part as its owner; the target leaves mixed-owner trees untouched."
    fi
}

check_kind_cert_ownership() {
    local repo_root
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    check_artifact_ownership "${repo_root}/bin/e2e-certs"
}

check_e2e_report_ownership() {
    local repo_root report_dir
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    report_dir="${REPORTS:-${repo_root}/reports}"
    check_artifact_ownership "${report_dir}"
}

check_podman
if [[ "${mode}" != "clean" && "${mode}" != "e2e-run" ]]; then
    check_kvm
fi

case "${mode}" in
    kind)
        check_cgroup_v2
        check_crun
        check_repo_bin_writable
        check_kind_cert_ownership
        check_user_systemd
        require_command systemd-run
        if ! systemd-run --scope --user --quiet -p Delegate=yes true >/dev/null 2>&1; then
            fail "systemd-run could not create a delegated user scope. Check user cgroup delegation and the user systemd manager; no system-wide delegation file is changed automatically."
        fi
        ;;
    e2e-prepare)
        check_crun
        check_repo_bin_writable
        check_e2e_artifact_ownership
        check_mock_artifact_ownership
        require_command virt-customize
        repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
        if [[ -n "${BREW_BUILD_URL:-}" ]] && \
            { [[ ! -x "${repo_root}/bin/flightctl" ]] || \
              [[ ! -x "${repo_root}/bin/flightctl-restore" ]] || \
              [[ ! -x "${repo_root}/bin/flightctl-backup" ]]; }; then
            require_command rpm2cpio
            require_command cpio
        fi
        ;;
    e2e-run)
        check_e2e_report_ownership
        # E2E runners consume prepared artifacts read-only. GO_E2E_DIRS and
        # Ginkgo filters can select API-only specs, so don't require local
        # preparation artifacts or host VM access here. VM-backed specs report
        # their own missing-image or libvirt errors when selected.
        ;;
    quadlets)
        check_cgroup_v2
        check_crun
        check_user_systemd
        check_user_systemd_kvm_access
        check_repo_bin_writable
        repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
        check_artifact_ownership "${repo_root}/bin/flightctl-standalone"
        ;;
    clean)
        # Cleanup touches only the current user's Podman store and needs no KVM.
        ;;
esac

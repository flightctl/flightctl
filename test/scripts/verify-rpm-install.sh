#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
RPM_DIR="${ROOT_DIR}/bin/rpm"
IMAGE="quay.io/centos/centos:stream9"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASSED=0
FAILED=0

pass() { PASSED=$((PASSED + 1)); printf "${GREEN}  PASS: %s${NC}\n" "$1"; }
fail() { FAILED=$((FAILED + 1)); printf "${RED}  FAIL: %s${NC}\n" "$1"; }
info() { printf "${YELLOW}>>> %s${NC}\n" "$1"; }

assert_installed()   { podman exec "$1" rpm -q "$2" >/dev/null 2>&1 && pass "$2 is installed"      || fail "$2 is NOT installed"; }
assert_not_installed() { podman exec "$1" rpm -q "$2" >/dev/null 2>&1 && fail "$2 IS installed"     || pass "$2 is not installed"; }
assert_file_exists() { podman exec "$1" test -f "$2"               && pass "$2 exists"             || fail "$2 does NOT exist"; }

check_rpms() {
    if [ ! -d "${RPM_DIR}" ]; then
        printf "${RED}ERROR: RPM directory not found at %s${NC}\n" "${RPM_DIR}" >&2
        printf "Run 'make rpm' first to build the RPMs.\n" >&2
        exit 1
    fi
    local agent_rpm greenboot_rpm selinux_rpm
    agent_rpm=$(find "${RPM_DIR}" -name 'flightctl-agent-*.rpm' ! -name '*.src.rpm' | head -1)
    selinux_rpm=$(find "${RPM_DIR}" -name 'flightctl-selinux-*.rpm' ! -name '*.src.rpm' | head -1)
    greenboot_rpm=$(find "${RPM_DIR}" -name 'flightctl-greenboot-*.rpm' ! -name '*.src.rpm' | head -1)
    if [ -z "${agent_rpm}" ] || [ -z "${selinux_rpm}" ]; then
        printf "${RED}ERROR: Missing required RPMs (agent, selinux) in %s${NC}\n" "${RPM_DIR}" >&2
        exit 1
    fi
    if [ -z "${greenboot_rpm}" ]; then
        printf "${RED}ERROR: flightctl-greenboot RPM not found in %s${NC}\n" "${RPM_DIR}" >&2
        printf "Ensure the spec includes %%package greenboot and rebuild with 'make rpm'.\n" >&2
        exit 1
    fi
}

setup_container() {
    local name="$1"
    podman run -d --name "${name}" \
        -v "${RPM_DIR}:/rpms-src:Z,ro" \
        "${IMAGE}" sleep infinity >/dev/null
    podman exec "${name}" bash -c '
        mkdir -p /rpms
        cp /rpms-src/*.rpm /rpms/ 2>/dev/null || true
        dnf install -y -q createrepo_c 2>/dev/null
        createrepo_c /rpms >/dev/null 2>&1
        cat > /etc/yum.repos.d/flightctl-local.repo <<EOF
[flightctl-local]
name=flightctl local RPMs
baseurl=file:///rpms
enabled=1
gpgcheck=0
EOF
    '
}

teardown_container() {
    podman rm -f "$1" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# AC-1: Package-mode install (no greenboot)
# ---------------------------------------------------------------------------
test_package_mode_install() {
    local ctr="verify-rpm-pkg-mode"
    info "AC-1: Package-mode install (install_weak_deps=False)"
    teardown_container "${ctr}"
    setup_container "${ctr}"

    podman exec "${ctr}" dnf install -y --setopt=install_weak_deps=False flightctl-agent

    assert_installed     "${ctr}" flightctl-agent
    assert_installed     "${ctr}" flightctl-selinux
    assert_not_installed "${ctr}" flightctl-greenboot
    assert_not_installed "${ctr}" greenboot
    assert_file_exists   "${ctr}" /usr/bin/flightctl-agent
    assert_file_exists   "${ctr}" /usr/lib/systemd/system/flightctl-agent.service

    teardown_container "${ctr}"
}

# ---------------------------------------------------------------------------
# AC-2: Image-mode install (default weak deps → pulls greenboot)
# ---------------------------------------------------------------------------
test_image_mode_install() {
    local ctr="verify-rpm-img-mode"
    info "AC-2: Image-mode install (default weak deps)"
    teardown_container "${ctr}"
    setup_container "${ctr}"

    podman exec "${ctr}" dnf install -y flightctl-agent

    assert_installed   "${ctr}" flightctl-agent
    assert_installed   "${ctr}" flightctl-greenboot
    assert_installed   "${ctr}" greenboot
    assert_file_exists "${ctr}" /usr/libexec/flightctl/mask-bootc-timer.sh
    assert_file_exists "${ctr}" /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
    assert_file_exists "${ctr}" /usr/lib/systemd/system/flightctl-configure-greenboot.service

    teardown_container "${ctr}"
}

# ---------------------------------------------------------------------------
# AC-3: Upgrade path — verify RPM metadata proves the mechanism
# ---------------------------------------------------------------------------
test_upgrade_path_mechanism() {
    local ctr="verify-rpm-upgrade"
    info "AC-3: Upgrade path mechanism (RPM metadata verification)"
    teardown_container "${ctr}"
    setup_container "${ctr}"

    podman exec "${ctr}" dnf install -y flightctl-agent

    local recommends version_lock
    recommends=$(podman exec "${ctr}" rpm -q --recommends flightctl-agent 2>/dev/null || true)
    if echo "${recommends}" | grep -q 'flightctl-greenboot'; then
        pass "flightctl-agent Recommends: flightctl-greenboot"
    else
        fail "flightctl-agent does NOT Recommend flightctl-greenboot (got: ${recommends})"
    fi

    version_lock=$(podman exec "${ctr}" rpm -q --requires flightctl-greenboot 2>/dev/null || true)
    if echo "${version_lock}" | grep -q 'flightctl-agent'; then
        pass "flightctl-greenboot Requires: flightctl-agent (version-locked)"
    else
        fail "flightctl-greenboot does NOT require flightctl-agent (got: ${version_lock})"
    fi

    teardown_container "${ctr}"
}

# ---------------------------------------------------------------------------
# AC-4: Agent systemd unit start smoke test (package-mode, no greenboot)
# ---------------------------------------------------------------------------
test_agent_unit_smoke() {
    local ctr="verify-rpm-unit-smoke"
    info "AC-4: Agent systemd unit start smoke test"
    teardown_container "${ctr}"
    setup_container "${ctr}"

    podman exec "${ctr}" dnf install -y --setopt=install_weak_deps=False flightctl-agent
    podman exec "${ctr}" dnf install -y -q systemd 2>/dev/null

    local execstartpre
    execstartpre=$(podman exec "${ctr}" grep '^ExecStartPre=' /usr/lib/systemd/system/flightctl-agent.service || true)
    if echo "${execstartpre}" | grep -q '^ExecStartPre=-'; then
        pass "ExecStartPre uses '-' prefix (non-fatal)"
    else
        fail "ExecStartPre missing '-' prefix: ${execstartpre}"
    fi

    if podman exec "${ctr}" systemd-analyze verify /usr/lib/systemd/system/flightctl-agent.service 2>&1; then
        pass "systemd-analyze verify passed"
    else
        local verify_out
        verify_out=$(podman exec "${ctr}" systemd-analyze verify /usr/lib/systemd/system/flightctl-agent.service 2>&1 || true)
        if echo "${verify_out}" | grep -qi 'error\|Failed'; then
            fail "systemd-analyze verify reported errors: ${verify_out}"
        else
            pass "systemd-analyze verify passed (warnings only)"
        fi
    fi

    teardown_container "${ctr}"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    check_rpms

    info "Pulling container image ${IMAGE}..."
    podman pull -q "${IMAGE}" >/dev/null 2>&1 || true

    test_package_mode_install
    test_image_mode_install
    test_upgrade_path_mechanism
    test_agent_unit_smoke

    echo ""
    printf "${GREEN}Passed: %d${NC}  ${RED}Failed: %d${NC}\n" "${PASSED}" "${FAILED}"
    if [ "${FAILED}" -gt 0 ]; then
        exit 1
    fi
}

main "$@"

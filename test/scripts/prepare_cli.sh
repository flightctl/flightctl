#!/bin/bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

if [[ -x "bin/flightctl" ]] && [[ -x "bin/flightctl-restore" ]] && [[ -x "bin/flightctl-backup" ]]; then
    echo -e "\e[32mCLI, flightctl-restore, and flightctl-backup already exist in bin/, skipping build\e[0m"
    exit 0
fi

if [[ "${EUID}" -ne 0 ]]; then
    if [[ -n "${BREW_BUILD_URL:-}" ]]; then
        echo -e "\e[32mDownloading and extracting the Brew CLI RPM into bin/ (no host installation)\e[0m"
        if ! download_brew_rpms "bin/brew-rpm"; then
            exit 1
        fi

        CLI_RPM="$(find bin/brew-rpm -maxdepth 1 -type f -name 'flightctl-*.rpm' -print \
            | grep -v -E '(agent|selinux|services|debug|\.src\.rpm)' | sort | head -n 1)"
        if [[ -z "${CLI_RPM}" ]]; then
            echo "ERROR: No flightctl CLI RPM found in Brew build ${BREW_BUILD_URL}" >&2
            echo "Available RPMs:" >&2
            ls -la bin/brew-rpm/*.rpm 2>/dev/null || echo "No RPMs found" >&2
            exit 1
        fi
        if ! command -v rpm2cpio >/dev/null 2>&1 || ! command -v cpio >/dev/null 2>&1; then
            echo "ERROR: Extracting a Brew CLI RPM without installing it requires rpm2cpio and cpio." >&2
            echo "Install those host tools, or unset BREW_BUILD_URL to compile the CLI from this checkout." >&2
            exit 1
        fi

        CLI_RPM="$(readlink -f "${CLI_RPM}")"
        EXTRACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flightctl-cli-rpm.XXXXXX")"
        trap 'rm -rf -- "${EXTRACT_DIR}"' EXIT
        (
            cd "${EXTRACT_DIR}"
            rpm2cpio "${CLI_RPM}" | cpio --extract --make-directories --quiet --no-absolute-filenames \
                './usr/bin/flightctl' './usr/bin/flightctl-restore' './usr/bin/flightctl-backup'
        )
        mkdir -p bin
        for binary in flightctl flightctl-restore flightctl-backup; do
            if [[ ! -f "${EXTRACT_DIR}/usr/bin/${binary}" ]]; then
                echo "ERROR: Brew CLI RPM ${CLI_RPM} did not contain /usr/bin/${binary}." >&2
                exit 1
            fi
            install -m 0755 "${EXTRACT_DIR}/usr/bin/${binary}" "bin/${binary}"
        done
        rm -rf -- "${EXTRACT_DIR}"
        trap - EXIT
    else
        if [[ -n "${FLIGHTCTL_RPM:-}" ]]; then
            echo "A non-root CLI preparation builds from this checkout; FLIGHTCTL_RPM=${FLIGHTCTL_RPM} is not installed on the host." >&2
            echo "Set BREW_BUILD_URL to extract an explicit built CLI RPM into bin/, or unset FLIGHTCTL_RPM to use the local build." >&2
        else
            echo -e "\e[32mCompiling the flightctl CLI, flightctl-restore, and flightctl-backup\e[0m"
        fi
        make build-cli build-restore build-backup
    fi
    exit 0
fi

if [[ -n "${BREW_BUILD_URL:-}" ]]; then
    echo -e "\e[32mInstalling the CLI from brew registry BREW_BUILD_URL: ${BREW_BUILD_URL}\e[0m"

    # Download all RPMs using shared function
    if ! download_brew_rpms "bin/brew-rpm"; then
        exit 1
    fi

    cd bin/brew-rpm

    # Find the CLI RPM (pattern: flightctl-<version>-<release>.<dist>.<arch>.rpm)
    # Examples: flightctl-0.9.1-1.el9fc.x86_64.rpm, flightctl-0.9.0-1.fc41.x86_64.rpm
    # Exclude: flightctl-agent*, flightctl-selinux*, flightctl-services*, flightctl-debug*, *.src.rpm
    CLI_RPM=$(ls flightctl-*.rpm 2>/dev/null | grep -v -E "(agent|selinux|services|debug|\.src\.rpm)" | head -1)

    if [[ -z "${CLI_RPM}" ]]; then
        echo "ERROR: No flightctl CLI RPM found in brew build ${BREW_BUILD_URL}"
        echo "Available RPMs:"
        ls -la flightctl-*.rpm 2>/dev/null || echo "No RPMs found"
        exit 1
    fi

    echo "Installing CLI RPM: ${CLI_RPM}"
    dnf remove -y flightctl || true
    dnf install -y "${CLI_RPM}"

    # copy to our local bin directory, where the remaining of tests will consume it from
    cp /usr/bin/flightctl ../flightctl
    cp /usr/bin/flightctl-restore ../flightctl-restore
    cp /usr/bin/flightctl-backup ../flightctl-backup

    cd - > /dev/null

elif [[ -z "${FLIGHTCTL_RPM}" ]]; then
    echo -e "\e[32mCompiling the flightctl CLI, flightctl-restore, and flightctl-backup\e[0m"
    make build-cli build-restore build-backup
else
    COPR_REPO=$(copr_repo)
    PACKAGE_CLI=$(package_cli)
    SYSVARIANT=$(rpm -qf /bin/bash | cut -d'.' -f 4) # el9, fc41, fc42, etc..: extracted from bash-5.2.32-1.fc41.x86_64

    if [[ "${PACKAGE_CLI}" != "flightctl" ]]; then
        PACKAGE_CLI="${PACKAGE_CLI}.${SYSVARIANT}"
    fi
    echo -e "\e[32mInstalling the CLI ${PACKAGE_CLI} rpm from copr ${COPR_REPO}, detected local system variant ${SYSVARIANT}\e[0m"

    # disable any existing copr repo that could have been enabled before
    dnf copr disable -y @redhat-et/flightctl 2>/dev/null || true
    dnf copr disable -y @redhat-et/flightctl-dev 2>/dev/null || true

    # enable the target corp repository
    dnf copr enable -y "$(copr_repo)"

    # dnf download doesn't work, so we rip out the rpm and install it manually
    dnf remove -y flightctl || true

    # if the package version has been specified, we must add the system variant to version
    # otherwise dnf can't download the right package

    dnf install -y "${PACKAGE_CLI}"

    # copy to our local bin directory, where the remaining of tests will consume it from
    cp /usr/bin/flightctl bin/flightctl
    cp /usr/bin/flightctl-restore bin/flightctl-restore
    cp /usr/bin/flightctl-backup bin/flightctl-backup
fi

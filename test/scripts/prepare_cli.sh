#!/bin/bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

if [[ -x "bin/flightctl" ]]; then
    echo -e "\e[32mCLI already exists at bin/flightctl, skipping build\e[0m"
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
    sudo dnf remove -y flightctl || true
    sudo dnf install -y "${CLI_RPM}"

    # copy to our local bin directory, where the remaining of tests will consume it from
    sudo cp /usr/bin/flightctl ../flightctl

    cd - > /dev/null

elif [[ -z "${FLIGHTCTL_RPM}" ]]; then
    echo -e "\e[32mCompiling the flightctl cli\e[0m"
    make build-cli
else
    COPR_REPO=$(copr_repo)
    PACKAGE_CLI=$(package_cli)
    SYSVARIANT=$(rpm -qf /bin/bash | cut -d'.' -f 4) # el9, fc41, fc42, etc..: extracted from bash-5.2.32-1.fc41.x86_64

    if [[ "${PACKAGE_CLI}" != "flightctl" ]]; then
        PACKAGE_CLI="${PACKAGE_CLI}.${SYSVARIANT}"
    fi
    echo -e "\e[32mInstalling the CLI ${PACKAGE_CLI} rpm from copr ${COPR_REPO}, detected local system variant ${SYSVARIANT}\e[0m"

    # disable any existing copr repo that could have been enabled before
    sudo dnf copr disable -y @redhat-et/flightctl 2>/dev/null || true
    sudo dnf copr disable -y @redhat-et/flightctl-dev 2>/dev/null || true

    # enable the target corp repository
    sudo dnf copr enable -y $(copr_repo)

    # dnf download doesn't work, so we rip out the rpm and install it manually
    sudo dnf remove -y flightctl || true

    # if the package version has been specified, we must add the system variant to version
    # otherwise dnf can't download the right package

    sudo dnf install -y "${PACKAGE_CLI}"

    # copy to our local bin directory, where the remaining of tests will consume it from
    sudo cp /usr/bin/flightctl bin/flightctl
fi
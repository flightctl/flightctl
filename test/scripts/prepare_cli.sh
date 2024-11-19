#!/bin/bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

if [[ -z "${FLIGHTCTL_RPM}" ]]; then
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
    cp /usr/bin/flightctl bin/flightctl
fi
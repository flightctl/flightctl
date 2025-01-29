#!/usr/bin/env bash

# if FLIGHTCTL_RPM is set, exit
if [ -n "${FLIGHTCTL_RPM:-}" ]; then
    echo "Skipping rpm build, as FLIGHTCTL_RPM is set to ${FLIGHTCTL_RPM}"
    rm bin/rpm/* 2>/dev/null || true
    exit 0
fi

# our RPM build process works in rpm bases systems so we wrap it if necessary
if ! command -v packit >/dev/null 2>&1; then
    echo "Building RPMs on a system without packit, using container"
    cat >bin/build_rpms.sh <<EOF
if ! dnf install -y go-rpm-macros; then
    echo "Failed to install go-rpm-macros package"
    exit 1
fi
git config --global --add safe.directory /work
./hack/build_rpms_packit.sh
EOF
    podman run --privileged --rm -t -v "$(pwd)":/work quay.io/flightctl/ci-rpm-builder:latest bash /work/bin/build_rpms.sh
else
    ./hack/build_rpms_packit.sh
fi

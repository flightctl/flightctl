#!/usr/bin/env bash
# our RPM build process works in rpm bases systems so we wrap it if necessary
if ! command -v packit 2>&1 >/dev/null ]; then
    echo "Building RPMs on a system without packtit, using container"
    cat >bin/build_rpms.sh <<EOF
#!/usr/bin/env bash
cd /work
./hack/build_rpms_packit.sh
EOF
    podman run --privileged --rm -t -v $(pwd):/work quay.io/flightctl/ci-rpm-builder:latest bash /work/bin/build_rpms.sh
else
    ./hack/build_rpms_packit.sh
fi


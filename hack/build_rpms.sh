#!/usr/bin/env bash
# our RPM build process works in rpm bases systems so we wrap it if necessary
if [ -f /etc/debian_version ]; then
    echo "Building RPMs on a non-rpm based system via podman..."
    cat >bin/build_rpms.sh <<EOF
#!/usr/bin/env bash
dnf install -y make packit golang openssl-devel
cd /work
./hack/build_rpms_packit.sh
EOF
    podman run --rm -t -v $(pwd):/work fedora:39 bash /work/bin/build_rpms.sh


else
    ./hack/build_rpms_packit.sh
fi


#!/bin/bash

set -oeux pipefail

NAME="flightctl"
SPEC="$(rpmspec -P "./packaging/rpm/${NAME}.spec")"
VERSION="$(echo "${SPEC}" | awk '/^Version:/ {print $2; exit}')"
URL="$(grep '^URL:' <<< "${SPEC}" | awk '{print $2}')"

cleanup() {
  rm -rf "${NAME}-${VERSION}" "${NAME}-${VERSION}.tar.gz" "${NAME}-${VERSION}-vendor.tar.bz2"
}
trap cleanup EXIT

git clone --depth 1 --branch "v${VERSION}" "${URL}" "${NAME}-${VERSION}"
[ $? -eq 0 ] || exit 1

tar czf "${NAME}-${VERSION}.tar.gz" \
	--sort=name \
	--mtime="@0" \
	--owner=0 \
	--group=0 \
	--no-same-permissions \
	--no-same-owner \
	--numeric-owner \
	"${NAME}-${VERSION}/"
[ $? -eq 0 ] || exit 1

pushd "${NAME}-${VERSION}/" >/dev/null || exit 1
if ! go mod vendor -v; then
    popd >/dev/null
    exit 1
fi
popd >/dev/null

tar cjSf "${NAME}-${VERSION}-vendor.tar.bz2" \
        --sort=name \
        --mtime="@0" \
        --owner=0 \
        --group=0 \
        --no-same-permissions \
        --no-same-owner \
        --numeric-owner \
        -C "${NAME}-${VERSION}/" go.mod go.sum vendor
[ $? -eq 0 ] || exit 1

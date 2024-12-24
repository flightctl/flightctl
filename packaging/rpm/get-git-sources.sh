#!/bin/sh

set -oeux pipefail

NAME="flightctl"
SPEC="$(rpmspec -P "${NAME}.spec")"
VERSION="$(grep '^Version:' <<< "${SPEC}" | awk '{print $2}')"
URL="$(grep '^URL:' <<< "${SPEC}" | awk '{print $2}')"

git clone --depth 1 --branch "v${VERSION}" "${URL}" "${NAME}-${VERSION}"

tar czf "${NAME}-${VERSION}.tar.gz" \
	--sort=name \
	--mtime="@0" \
	--owner=0 \
	--group=0 \
	--no-same-permissions \
	--no-same-owner \
	--numeric-owner \
	"${NAME}-${VERSION}/"

cd "${NAME}-${VERSION}/" && go mod vendor -v && cd -

tar cjSf "${NAME}-${VERSION}-vendor.tar.bz2" \
        --sort=name \
        --mtime="@0" \
        --owner=0 \
        --group=0 \
        --no-same-permissions \
        --no-same-owner \
        --numeric-owner \
        -C "${NAME}-${VERSION}/" go.mod go.sum vendor

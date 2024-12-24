#!/bin/sh

set -oeux pipefail

NAME="flightctl"
SPEC="$(rpmspec -P "${NAME}.spec")"
VERSION="$(grep '^Version:' <<< "${SPEC}" | awk '{print $2}')"

spectool --get-files "${NAME}.spec"

tar xf "${NAME}-${VERSION}.tar.gz"

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

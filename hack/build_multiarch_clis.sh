#!/usr/bin/env bash
set -euo pipefail

# This script generates an archive of CLI binary files in the following format:
#
# $ tree bin/clis/
# bin/clis/
# ├── archives
# │   ├── amd64
# │   │   ├── linux
# │   │   │   └── flightctl.tar.gz
# │   │   └── mac
# │   │       └── flightctl.zip
# │   └── arm64
# │       ├── linux
# │       │   └── flightctl.tar.gz
# │       └── mac
# │           └── flightctl.zip
# ├── gh-archives
# │   ├── flightctl-darwin-amd64.zip
# │   ├── flightctl-darwin-amd64.zip.sha256
# │   ├── flightctl-darwin-arm64.zip
# │   ├── flightctl-darwin-arm64.zip.sha256
# │   ├── flightctl-linux-amd64.tar.gz
# │   ├── flightctl-linux-amd64.tar.gz.sha256
# │   ├── flightctl-linux-arm64.tar.gz
# │   └── flightctl-linux-arm64.tar.gz.sha256


for GOARCH in amd64 arm64; do
  for GOOS in linux darwin; do
    echo -e "\033[0;37m>>>> building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
    GOARCH="${GOARCH}" GOOS="${GOOS}" make build-cli
    OS="${GOOS}"
    if [ "${GOOS}" == "darwin" ]; then
      OS="mac"
    fi

    mkdir -p "bin/clis/tmp/${GOARCH}/${GOOS}"
    cp "bin/flightctl" "bin/clis/tmp/${GOARCH}/${GOOS}/flightctl"
    mkdir -p "bin/clis/archives/${GOARCH}/${OS}"
    mkdir -p "bin/clis/gh-archives/"
    if [ "${GOOS}" == "linux" ]; then
      tar -zhcf "bin/clis/archives/${GOARCH}/${OS}/flightctl.tar.gz" -C "bin/clis/tmp/${GOARCH}/${GOOS}" flightctl
      GH_OUT="bin/clis/gh-archives/flightctl-${GOOS}-${GOARCH}.tar.gz"
      cp "bin/clis/archives/${GOARCH}/${OS}/flightctl.tar.gz" "${GH_OUT}"
    else
      zip -9 -r -q -j "bin/clis/archives/${GOARCH}/${OS}/flightctl.zip" "bin/clis/tmp/${GOARCH}/${GOOS}/flightctl"
      GH_OUT="bin/clis/gh-archives/flightctl-${GOOS}-${GOARCH}.zip"
      cp "bin/clis/archives/${GOARCH}/${OS}/flightctl.zip" "${GH_OUT}"
    fi
    sha256sum "${GH_OUT}" | awk '{ print $1 }' > "${GH_OUT}.sha256"
  done
done

echo -e "\033[0;32mAll CLI binaries have been built and archived in bin/clis/archives and bin/clis/gh-archives\033[0m"

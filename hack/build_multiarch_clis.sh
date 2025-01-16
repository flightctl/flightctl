#!/usr/bin/env bash
set -euo pipefail
set -x
# This script generates an archive of CLI binary files in the following format:
#
# $ tree bin/clis/
# bin/clis/
# ├── binaries
# │   ├── amd64
# │   │   ├── linux
# │   │   │   └── flightctl
# │   │   ├── mac
# │   │   │   └── flightctl
# │   │   └── windows
# │   │       └── flightctl.exe
# │   └── arm64
# │       ├── linux
# │       │   └── flightctl
# │       ├── mac
# │       │   └── flightctl
# │       └── windows
# │           └── flightctl.exe
# ├── archives
# │   ├── amd64
# │   │   ├── linux
# │   │   │   └── flightctl.tar.gz
# │   │   ├── mac
# │   │   │   └── flightctl.zip
# │   │   └── windows
# │   │       └── flightctl.zip
# │   └── arm64
# │       ├── linux
# │       │   └── flightctl.tar.gz
# │       ├── mac
# │       │   └── flightctl.zip
# │       └── windows
# │           └── flightctl.zip
# ├── gh-archives
# │   ├── flightctl-darwin-amd64.zip
# │   ├── flightctl-darwin-amd64.zip.sha256
# │   ├── flightctl-darwin-arm64.zip
# │   ├── flightctl-darwin-arm64.zip.sha256
# │   ├── flightctl-windows-amd64.zip
# │   ├── flightctl-windows-amd64.zip.sha256
# │   ├── flightctl-windows-arm64.zip
# │   ├── flightctl-windows-arm64.zip.sha256
# │   ├── flightctl-linux-amd64.tar.gz
# │   ├── flightctl-linux-amd64.tar.gz.sha256
# │   ├── flightctl-linux-arm64.tar.gz
# │   └── flightctl-linux-arm64.tar.gz.sha256


for GOARCH in amd64 arm64; do
  for GOOS in linux darwin windows; do
    echo -e "\033[0;37m>>>> building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
    GOARCH="${GOARCH}" GOOS="${GOOS}" make build-cli
    OS="${GOOS}"
    TGZ=".tar.gz"
    EXE=""
    if [ "${GOOS}" == "darwin" ]; then
      OS="mac"
      TGZ=".zip"
    elif [ "${GOOS}" == "windows" ]; then
      TGZ=".zip"
      EXE=".exe"
    fi
    BIN="bin/clis/binaries/${GOARCH}/${OS}"
    ARCHIVES="bin/clis/archives/${GOARCH}/${OS}"
    GH_ARCHIVES="bin/clis/gh-archives/${GOARCH}/${OS}"
    GH_OUT="${GH_ARCHIVES}/flightctl-${GOOS}-${GOARCH}${TGZ}"

    mkdir -p "${BIN}"
    mkdir -p "${ARCHIVES}"
    mkdir -p "${GH_ARCHIVES}"

    cp "bin/flightctl${EXE}" "${BIN}/"
    cp "bin/flightctl${EXE}" "flightctl-${GOOS}-${GOARCH}${EXE}"
    if [ "${GOOS}" == "linux" ]; then
      tar -zhcf "${ARCHIVES}/flightctl.tar.gz" -C "${BIN}" flightctl
    else
      zip -9 -r -q -j "${ARCHIVES}/flightctl.zip" "${BIN}/flightctl${EXE}"
    fi
    cp "${ARCHIVES}/flightctl${TGZ}" "${GH_OUT}"
    sha256sum "${GH_OUT}" | awk '{ print $1 }' > "${GH_OUT}.sha256"
  done
done

echo -e "\033[0;32mAll CLI binaries have been built in bin/cli/binaries and archived in bin/clis/archives and bin/clis/gh-archives\033[0m"

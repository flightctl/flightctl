#!/usr/bin/env bash
set -euo pipefail

# This script builds downloadable CLI tools for multiple OS/arch combinations, lays out
# archives under bin/clis/, and writes gh-archives/index.json.
#
# Each tool is built sequentially for every platform (GOARCH, GOOS) before the next tool.
#
# Directory layout:
#
# $ tree bin/clis/
# bin/clis/
# ├── archives
# │   ├── amd64
# │   │   ├── linux
# │   │   │   ├── flightctl.tar.gz
# │   │   │   ├── flightctl-restore.tar.gz
# │   │   │   └── flightctl-backup.tar.gz
# │   │   ├── mac
# │   │   │   ├── flightctl.zip
# │   │   │   ├── flightctl-restore.zip
# │   │   │   └── flightctl-backup.zip
# │   │   └── windows
# │   │       ├── flightctl.zip
# │   │       ├── flightctl-restore.zip
# │   │       └── flightctl-backup.zip
# │   └── arm64
# │       ├── linux
# │       │   ├── flightctl.tar.gz
# │       │   ├── flightctl-restore.tar.gz
# │       │   └── flightctl-backup.tar.gz
# │       ├── mac
# │       │   ├── flightctl.zip
# │       │   ├── flightctl-restore.zip
# │       │   └── flightctl-backup.zip
# │       └── windows
# │           ├── flightctl.zip
# │           ├── flightctl-restore.zip
# │           └── flightctl-backup.zip
# ├── binaries
# │   ├── amd64
# │   │   ├── linux
# │   │   │   ├── flightctl
# │   │   │   ├── flightctl-restore
# │   │   │   └── flightctl-backup
# │   │   ├── mac
# │   │   │   ├── flightctl
# │   │   │   ├── flightctl-restore
# │   │   │   └── flightctl-backup
# │   │   └── windows
# │   │       ├── flightctl.exe
# │   │       ├── flightctl-restore.exe
# │   │       └── flightctl-backup.exe
# │   └── arm64
# │       ├── linux
# │       │   ├── flightctl
# │       │   ├── flightctl-restore
# │       │   └── flightctl-backup
# │       ├── mac
# │       │   ├── flightctl
# │       │   ├── flightctl-restore
# │       │   └── flightctl-backup
# │       └── windows
# │           ├── flightctl.exe
# │           ├── flightctl-restore.exe
# │           └── flightctl-backup.exe
# └── gh-archives
#     ├── index.json   (artifact manifest)
#     ├── amd64
#     │   ├── linux
#     │   │   ├── flightctl-linux-amd64.tar.gz (+ .sha256)
#     │   │   ├── flightctl-restore-linux-amd64.tar.gz (+ .sha256)
#     │   │   └── flightctl-backup-linux-amd64.tar.gz (+ .sha256)
#     │   ├── mac
#     │   │   ├── flightctl-darwin-amd64.zip (+ .sha256)
#     │   │   ├── flightctl-restore-darwin-amd64.zip (+ .sha256)
#     │   │   └── flightctl-backup-darwin-amd64.zip (+ .sha256)
#     │   └── windows
#     │       ├── flightctl-windows-amd64.zip (+ .sha256)
#     │       ├── flightctl-restore-windows-amd64.zip (+ .sha256)
#     │       └── flightctl-backup-windows-amd64.zip (+ .sha256)
#     └── arm64
#         ├── linux
#         │   ├── flightctl-linux-arm64.tar.gz (+ .sha256)
#         │   ├── flightctl-restore-linux-arm64.tar.gz (+ .sha256)
#         │   └── flightctl-backup-linux-arm64.tar.gz (+ .sha256)
#         ├── mac
#         │   ├── flightctl-darwin-arm64.zip (+ .sha256)
#         │   ├── flightctl-restore-darwin-arm64.zip (+ .sha256)
#         │   └── flightctl-backup-darwin-arm64.zip (+ .sha256)
#         └── windows
#             ├── flightctl-windows-arm64.zip (+ .sha256)
#             ├── flightctl-restore-windows-arm64.zip (+ .sha256)
#             └── flightctl-backup-windows-arm64.zip (+ .sha256)
#
# When adding a new CLI, each index.json row includes "tool" set to that binary name.
CLI_TOOLS=(flightctl flightctl-restore flightctl-backup)

get_cli_make_target() {
  case "$1" in
    flightctl) echo build-cli ;;
    flightctl-restore) echo build-restore ;;
    flightctl-backup) echo build-backup ;;
    *)
      echo "Unknown downloadable CLI '$1': add a Makefile target mapping in get_cli_make_target()." >&2
      exit 1
      ;;
  esac
}

# Build and archive one CLI tool for a specific platform
build_platform_cli() {
  local CLI=$1
  local GOARCH=$2
  local GOOS=$3
  local make_target=$4

  DISABLE_FIPS=true GOARCH="${GOARCH}" GOOS="${GOOS}" make "${make_target}"

  local OS="${GOOS}"
  local TGZ=".tar.gz"
  local EXE=""

  if [ "${GOOS}" == "darwin" ]; then
    OS="mac"
    TGZ=".zip"
  elif [ "${GOOS}" == "windows" ]; then
    TGZ=".zip"
    EXE=".exe"
  fi

  local BIN="bin/clis/binaries/${GOARCH}/${OS}"
  local ARCHIVES="bin/clis/archives/${GOARCH}/${OS}"
  local GH_ARCHIVES="bin/clis/gh-archives/${GOARCH}/${OS}"

  mkdir -p "${BIN}" "${ARCHIVES}" "${GH_ARCHIVES}"

  cp "bin/${CLI}${EXE}" "${BIN}/"
  cp "bin/${CLI}${EXE}" "${CLI}-${GOOS}-${GOARCH}${EXE}"

  if [ "${GOOS}" == "linux" ]; then
    tar -zhcf "${ARCHIVES}/${CLI}.tar.gz" -C "${BIN}" "${CLI}"
  else
    zip -9 -r -q -j "${ARCHIVES}/${CLI}.zip" "${BIN}/${CLI}${EXE}"
  fi

  local GH_OUT="${GH_ARCHIVES}/${CLI}-${GOOS}-${GOARCH}${TGZ}"
  cp "${ARCHIVES}/${CLI}${TGZ}" "${GH_OUT}"
  sha256sum "${GH_OUT}" | awk '{ print $1 }' > "${GH_OUT}.sha256"

  local sha256_content filename
  sha256_content=$(cat "${GH_OUT}.sha256")
  filename=$(basename "${GH_OUT}")
  jq -n -c \
    --arg os "${OS}" \
    --arg arch "${GOARCH}" \
    --arg filename "${filename}" \
    --arg sha256 "${sha256_content}" \
    --arg tool "${CLI}" \
    '{os: $os, arch: $arch, filename: $filename, sha256: $sha256, tool: $tool}' >>"${ARTIFACTS_JSONL}"
}

# The artifacts JSONL file is built as a stream of JSON objects, one per archive file.
ARTIFACTS_JSONL=$(mktemp)
trap 'rm -f "${ARTIFACTS_JSONL}"' EXIT

echo -e "\033[0;37m>>>> Building multi-arch CLI archives\033[0m"

for CLI in "${CLI_TOOLS[@]}"; do
  cli_make_target=$(get_cli_make_target "${CLI}")
  echo -e "\033[0;37m>>>> Start all platforms for CLI=${CLI} (${cli_make_target})\033[0m"
  for GOARCH in amd64 arm64; do
    for GOOS in linux darwin windows; do
      echo -e "\033[0;37m>>>>   GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
      build_platform_cli "${CLI}" "${GOARCH}" "${GOOS}" "${cli_make_target}"
    done
  done
  echo -e "\033[0;37m>>>> Finished all platforms for CLI=${CLI}\033[0m"
done

echo -e "\033[0;37m>>>> writing index.json\033[0m"

OUTPUT_JSON="bin/clis/gh-archives/index.json"
PLACEHOLDER_BASE_URL="{{CLI_ARTIFACTS_BASE_URL}}"

mkdir -p "$(dirname "${OUTPUT_JSON}")"
artifacts=$(jq -s '.' "${ARTIFACTS_JSONL}")

jq -n \
  --arg baseUrl "$PLACEHOLDER_BASE_URL" \
  --argjson artifacts "$artifacts" \
  '{baseUrl: $baseUrl, artifacts: $artifacts}' >"${OUTPUT_JSON}"

echo -e "\033[0;32mAll CLI binaries have been built in bin/clis/binaries and archived in bin/clis/archives and bin/clis/gh-archives\033[0m"

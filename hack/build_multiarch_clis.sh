#!/usr/bin/env bash
set -euo pipefail

# This script builds flightctl and flightctl-restore for multiple OS/arch combinations
# and generates archives in the following layout:
#
# $ tree bin/clis/
# bin/clis/
# ├── archives
# │   ├── amd64
# │   │   ├── linux
# │   │   │   ├── flightctl.tar.gz
# │   │   │   └── flightctl-restore.tar.gz
# │   │   ├── mac
# │   │   │   ├── flightctl.zip
# │   │   │   └── flightctl-restore.zip
# │   │   └── windows
# │   │       ├── flightctl.zip
# │   │       └── flightctl-restore.zip
# │   └── arm64
# │       ├── linux
# │       │   ├── flightctl.tar.gz
# │       │   └── flightctl-restore.tar.gz
# │       ├── mac
# │       │   ├── flightctl.zip
# │       │   └── flightctl-restore.zip
# │       └── windows
# │           ├── flightctl.zip
# │           └── flightctl-restore.zip
# ├── binaries
# │   ├── amd64
# │   │   ├── linux
# │   │   │   ├── flightctl
# │   │   │   └── flightctl-restore
# │   │   ├── mac
# │   │   │   ├── flightctl
# │   │   │   └── flightctl-restore
# │   │   └── windows
# │   │       ├── flightctl.exe
# │   │       └── flightctl-restore.exe
# │   └── arm64
# │       ├── linux
# │       │   ├── flightctl
# │       │   └── flightctl-restore
# │       ├── mac
# │       │   ├── flightctl
# │       │   └── flightctl-restore
# │       └── windows
# │           ├── flightctl.exe
# │           └── flightctl-restore.exe
# └── gh-archives
#     ├── amd64
#     │   ├── linux
#     │   │   ├── flightctl-linux-amd64.tar.gz (+ .sha256)
#     │   │   └── flightctl-restore-linux-amd64.tar.gz (+ .sha256)
#     │   ├── mac
#     │   │   ├── flightctl-darwin-amd64.zip (+ .sha256)
#     │   │   └── flightctl-restore-darwin-amd64.zip (+ .sha256)
#     │   └── windows
#     │       ├── flightctl-windows-amd64.zip (+ .sha256)
#     │       └── flightctl-restore-windows-amd64.zip (+ .sha256)
#     └── arm64
#         ├── linux
#         │   ├── flightctl-linux-arm64.tar.gz (+ .sha256)
#         │   └── flightctl-restore-linux-arm64.tar.gz (+ .sha256)
#         ├── mac
#         │   ├── flightctl-darwin-arm64.zip (+ .sha256)
#         │   └── flightctl-restore-darwin-arm64.zip (+ .sha256)
#         └── windows
#             ├── flightctl-windows-arm64.zip (+ .sha256)
#             └── flightctl-restore-windows-arm64.zip (+ .sha256)

build() {
  local GOARCH=$1
  local GOOS=$2

  DISABLE_FIPS=true GOARCH="${GOARCH}" GOOS="${GOOS}" make build-cli build-restore

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

  for CLI in flightctl flightctl-restore; do
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
  done
}

for GOARCH in amd64 arm64; do
  for GOOS in linux darwin windows; do
    echo -e "\033[0;37m>>>> Start building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
    build "$GOARCH" "$GOOS"
    echo -e "\033[0;37m>>>> Finish building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
  done
done

echo -e "\033[0;37m>>>> building index.json\033[0m"

SOURCE_DIR="bin/clis/gh-archives"
OUTPUT_JSON="index.json"
PLACEHOLDER_BASE_URL="{{CLI_ARTIFACTS_BASE_URL}}"

cd "$SOURCE_DIR"

# Build JSON array of artifact entries
artifacts=$(find . -type f | while read -r file; do
  filename=$(basename "$file")

  # Skip index.json and .sha256 files
  [[ "$filename" == "$OUTPUT_JSON" || "$filename" == *.sha256 ]] && continue

  # Extract arch and os from path
  parts=(${file//\// })
  arch="${parts[1]}"
  os="${parts[2]}"

  # Read SHA256 if it exists
  sha256_file="${file}.sha256"
  sha256_content=""
  [[ -f "$sha256_file" ]] && sha256_content=$(cat "$sha256_file")

  # Emit artifact JSON object
  jq -n \
    --arg os "$os" \
    --arg arch "$arch" \
    --arg filename "$filename" \
    --arg sha256 "$sha256_content" \
    '{
      os: $os,
      arch: $arch,
      filename: $filename,
      sha256: $sha256
    }'
done | jq -s '.')

# Write final JSON with baseUrl as placeholder
jq -n \
  --arg baseUrl "$PLACEHOLDER_BASE_URL" \
  --argjson artifacts "$artifacts" \
  '{baseUrl: $baseUrl, artifacts: $artifacts}' > "$OUTPUT_JSON"

cd -

echo -e "\033[0;32mAll CLI binaries have been built in bin/clis/binaries and archived in bin/clis/archives and bin/clis/gh-archives\033[0m"

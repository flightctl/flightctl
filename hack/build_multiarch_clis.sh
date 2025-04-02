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
# └── gh-archives
#     ├── amd64
#     │   ├── linux
#     │   │   ├── flightctl-linux-amd64.tar.gz
#     │   │   └── flightctl-linux-amd64.tar.gz.sha256
#     │   ├── mac
#     │   │   ├── flightctl-darwin-amd64.zip
#     │   │   └── flightctl-darwin-amd64.zip.sha256
#     │   └── windows
#     │       ├── flightctl-windows-amd64.zip
#     │       └── flightctl-windows-amd64.zip.sha256
#     └── arm64
#         ├── linux
#         │   ├── flightctl-linux-arm64.tar.gz
#         │   └── flightctl-linux-arm64.tar.gz.sha256
#         ├── mac
#         │   ├── flightctl-darwin-arm64.zip
#         │   └── flightctl-darwin-arm64.zip.sha256
#         └── windows
#             ├── flightctl-windows-arm64.zip
#             └── flightctl-windows-arm64.zip.sha256

build() {
  local GOARCH=$1
  local GOOS=$2

  GOARCH="${GOARCH}" GOOS="${GOOS}" make build-cli

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
  local GH_OUT="${GH_ARCHIVES}/flightctl-${GOOS}-${GOARCH}${TGZ}"

  mkdir -p "${BIN}" "${ARCHIVES}" "${GH_ARCHIVES}"

  cp "bin/flightctl${EXE}" "${BIN}/"
  cp "bin/flightctl${EXE}" "flightctl-${GOOS}-${GOARCH}${EXE}"

  if [ "${GOOS}" == "linux" ]; then
    tar -zhcf "${ARCHIVES}/flightctl.tar.gz" -C "${BIN}" flightctl
  else
    zip -9 -r -q -j "${ARCHIVES}/flightctl.zip" "${BIN}/flightctl${EXE}"
  fi

  cp "${ARCHIVES}/flightctl${TGZ}" "${GH_OUT}"
  sha256sum "${GH_OUT}" | awk '{ print $1 }' > "${GH_OUT}.sha256"
}

for GOARCH in amd64 arm64; do
  for GOOS in linux darwin windows; do
    (
      echo -e "\033[0;37m>>>> Start building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
      build "$GOARCH" "$GOOS"
      echo -e "\033[0;37m>>>> Finish building cli for GOARCH=${GOARCH} GOOS=${GOOS}\033[0m"
    ) &> "build_${GOOS}_${GOARCH}.log" &
  done
done

# Wait for all parallel jobs to complete
wait
cat build_*.log
rm -f build_*.log


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

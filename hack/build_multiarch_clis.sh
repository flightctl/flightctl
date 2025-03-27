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

echo -e "\033[0;37m>>>> building index.json\033[0m"
# BASE_URL should be set in environment
SOURCE_DIR="bin/clis/gh-archives"
OUTPUT_JSON="index.json"

cd $SOURCE_DIR

echo '{"links": [' > $OUTPUT_JSON
find . -type f | while read -r file; do
  # Extract architecture and OS from path
  arch=$(echo "$file" | awk -F'/' '{print $2}')
  os=$(echo "$file" | awk -F'/' '{print $3}')
  filename=$(echo "$file" | awk -F'/' '{print $NF}')

  # Skip an index.json file
  if [[ "$filename" == "$OUTPUT_JSON" ]]; then
      continue
  fi

  # Skip a SHA256 file
  if [[ "$filename" == *.sha256 ]]; then
      continue
  fi

  # Corresponding SHA256 file
  sha256_file="${file}.sha256"
  if [[ -f "$sha256_file" ]]; then
      sha256_file="${filename}.sha256"
  else
      sha256_file=""
  fi

  # Construct JSON entry
  echo "  {" >> $OUTPUT_JSON
  echo "    \"os\": \"$os\"," >> $OUTPUT_JSON
  echo "    \"arch\": \"$arch\"," >> $OUTPUT_JSON
  echo "    \"url\": \"$BASE_URL/$filename\"," >> $OUTPUT_JSON
  echo "    \"sha256\": \"$BASE_URL/$sha256_file\"" >> $OUTPUT_JSON
  echo "  }," >> $OUTPUT_JSON
done
# Remove last comma and close JSON
sed -i '$ s/,$//' $OUTPUT_JSON
echo "]}" >> $OUTPUT_JSON

cd -

echo -e "\033[0;32mAll CLI binaries have been built in bin/clis/binaries and archived in bin/clis/archives and bin/clis/gh-archives\033[0m"

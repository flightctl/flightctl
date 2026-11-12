#!/usr/bin/env bash
set -euo pipefail

arch="${1:-$(uname -m)}"
output_root="${2:-/opt/flightctl-go-rpm}"

download_go_rpm() {
  local release="$1"
  local release_suffix="$2"
  local package_kind="$3"
  local package_arch="$4"
  local package="${package_kind}-1.26.10-1.${release_suffix}.${package_arch}.rpm"
  local url="https://cdn-ubi.redhat.com/content/public/ubi/dist/ubi${release}/${release}/${arch}/appstream/os/Packages/g/${package}"
  local destination="${output_root}/${release}/${arch}"
  local nevra

  mkdir -p "${destination}"
  curl --fail --location --retry 3 --output "${destination}/${package}" "${url}"
  rpm -K "${destination}/${package}" >/dev/null
  nevra="$(rpm -qp --queryformat '%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\n' "${destination}/${package}")"
  if [[ "${nevra}" != "${package_kind}-1.26.10-1.${release_suffix}.${package_arch}" ]]; then
    echo "Unexpected Go RPM: ${nevra}" >&2
    exit 1
  fi
}

for package_kind in golang golang-bin golang-race; do
  download_go_rpm 9 "el9_8" "${package_kind}" "${arch}"
  download_go_rpm 10 "el10_2" "${package_kind}" "${arch}"
done

download_go_rpm 9 "el9_8" golang-src noarch
download_go_rpm 10 "el10_2" golang-src noarch

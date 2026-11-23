#!/usr/bin/env bash
# Build the updater contract fixture delivery image for one Linux platform.
#
# Writes to the output directory: context/ (the staged /updater tree),
# descriptor-inputs.json, report.json, build-info.txt, an OCI image layout in
# oci/ and the OCI image manifest digest in image.digest. The image is also
# tagged in the local container storage that the environment selects. No
# registry is contacted.
set -euo pipefail

usage() {
	echo "usage: $0 --arch <amd64|arm64> --output <empty directory> [--revision <commit>] [--version <version>] [--image <name>]" >&2
	exit 2
}

arch=""
output=""
revision="HEAD"
version="0.1.0"
image="localhost/updater-contract-fixture"
while [ "$#" -gt 0 ]; do
	[ "$#" -ge 2 ] || usage
	case "$1" in
	--arch) arch="$2" ;;
	--output) output="$2" ;;
	--revision) revision="$2" ;;
	--version) version="$2" ;;
	--image) image="$2" ;;
	*) usage ;;
	esac
	shift 2
done
case "$arch" in
amd64 | arm64) ;;
*) usage ;;
esac
[ -n "$output" ] || usage

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
head="$(git -C "$root" rev-parse --verify 'HEAD^{commit}')"
revision="$(git -C "$root" rev-parse --verify "${revision}^{commit}")"
if [ "$revision" != "$head" ]; then
	echo "revision ${revision} is not the checked-out commit ${head}; check it out before building" >&2
	exit 1
fi
source_date_epoch="$(git -C "$root" show -s --format=%ct "$revision")"
created="$(date -u -d "@${source_date_epoch}" +%Y-%m-%dT%H:%M:%SZ)"
changes="$(git -C "$root" status --porcelain -- test/fixtures/firmware-updater packaging/images/firmware-updater go.mod go.sum)"
if [ -n "$changes" ]; then
	echo "warning: the build inputs have local changes; the image does not reproduce ${revision}" >&2
fi
# Build with the Go version that go.mod pins and fixed code generation settings.
go_toolchain="go$(awk '$1 == "go" { print $2; exit }' "${root}/go.mod")"
go_env=(env GOTOOLCHAIN="$go_toolchain" GOFLAGS= GOEXPERIMENT= GOAMD64=v1 GOARM64=v8.0)

mkdir -p "$output"
output="$(cd "$output" && pwd)"
if [ -n "$(ls -A "$output")" ]; then
	echo "output directory ${output} must be empty" >&2
	exit 1
fi
umask 022
name="updater-contract-fixture-${arch}"
tag="${image}:${version}-linux-${arch}"

{
	echo "revision=${revision}"
	echo "source_date_epoch=${source_date_epoch}"
	echo "go=$(cd "$root" && "${go_env[@]}" go env GOVERSION) goamd64=v1 goarm64=v8.0 cgo=0"
	echo "podman=$(podman version --format '{{.Client.Version}}')"
} >"${output}/build-info.txt"
(cd "$root" && "${go_env[@]}" CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
	-ldflags="-s -w -buildid=" -o "${output}/updater" ./test/fixtures/firmware-updater)
(cd "$root" && "${go_env[@]}" go run ./packaging/images/firmware-updater \
	--binary "${output}/updater" --schemas test/fixtures/firmware-updater/schemas \
	--name "$name" --version "$version" --arch "$arch" --revision "$revision" \
	--context "${output}/context" --descriptor "${output}/descriptor-inputs.json" --report "${output}/report.json")
rm -f "${output}/updater"

podman build --no-cache --pull=never --format oci --platform "linux/${arch}" \
	--timestamp "$source_date_epoch" --identity-label=false \
	--label "org.opencontainers.image.source=https://github.com/flightctl/flightctl" \
	--label "org.opencontainers.image.revision=${revision}" \
	--label "org.opencontainers.image.version=${version}" \
	--label "org.opencontainers.image.created=${created}" \
	--label "io.flightctl.updater.platform=linux/${arch}" \
	--file "${root}/packaging/images/firmware-updater/Containerfile" --tag "$tag" "${output}/context"
podman push --format oci --compression-format gzip --force-compression \
	--digestfile "${output}/image.digest" "$tag" "oci:${output}/oci:${version}-linux-${arch}"
echo "${tag} $(cat "${output}/image.digest")"

#!/usr/bin/env bash
set -euo pipefail

# Upload images from a bundle tar to a registry.
# Usage: ./upload-images.sh <bundle.tar> [--registry-endpoint host:port] [--jobs N]
#
# Agent bundles are an OCI layout plus e2e-refs.tsv (manifest digests preserved).
# App bundles are docker-archive.
#
# If REGISTRY_ENDPOINT is not provided, it will be calculated using registry_address()
#
# Anonymous / no-auth push (e.g. local insecure registry): set DEST_REGISTRY_NO_CREDS=1
# so skopeo gets --dest-no-creds (avoids reading auth.json for the destination).

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}/../../functions"

BUNDLE=""
ARG_ENDPOINT=""

if [ -z "${JOBS:-}" ]; then
  NPROC=$(nproc)
  JOBS=$((NPROC < 4 ? NPROC : 4))
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --registry-endpoint) ARG_ENDPOINT="$2"; shift 2 ;;
    --jobs) JOBS="$2"; shift 2 ;;
    -*)
      echo "unknown arg: $1"; exit 2 ;;
    *)
      if [ -z "${BUNDLE}" ]; then
        BUNDLE="$1"
      else
        echo "unknown arg: $1"; exit 2
      fi
      shift
      ;;
  esac
done

if [ -z "${BUNDLE}" ]; then
  echo "Usage: $0 <bundle.tar> [--registry-endpoint host:port] [--jobs N]"
  exit 1
fi

REGISTRY_ENDPOINT="${REGISTRY_ENDPOINT:-$ARG_ENDPOINT}"
if [ -z "${REGISTRY_ENDPOINT}" ]; then
  REGISTRY_ENDPOINT=$(registry_address)
  echo "Using calculated registry address: ${REGISTRY_ENDPOINT}"
fi

check_registry "${REGISTRY_ENDPOINT}"

if [[ "${DEST_REGISTRY_NO_CREDS:-}" == "1" || "${DEST_REGISTRY_NO_CREDS:-}" == "true" ]]; then
  echo "Registry push: using skopeo --dest-no-creds (DEST_REGISTRY_NO_CREDS=${DEST_REGISTRY_NO_CREDS})"
fi

echo "Pushing images from bundle: ${BUNDLE}"

dest_no_creds() {
  [[ "${DEST_REGISTRY_NO_CREDS:-}" == "1" || "${DEST_REGISTRY_NO_CREDS:-}" == "true" ]]
}

copy_retry() {
  local src="$1"
  local dst="$2"
  shift 2
  local extra_flags=("$@")
  local tag="${dst##*:}"
  local pfx="[push ${tag}] "
  echo "${pfx}${src} -> ${dst}"

  local max_retries=3
  local retry=0
  local skopeo_output skopeo_exit
  local dest_flags=(--dest-tls-verify=false)
  if dest_no_creds; then
    dest_flags+=(--dest-no-creds)
  fi
  while [[ $retry -lt $max_retries ]]; do
    set +euo pipefail
    skopeo_output=$(skopeo copy "${dest_flags[@]}" "${extra_flags[@]}" "$src" "$dst" 2>&1)
    skopeo_exit=$?
    echo "$skopeo_output" | awk -v p="$pfx" "{print p \$0}"
    if [[ $skopeo_exit -eq 0 ]]; then
      set -euo pipefail
      return 0
    fi
    ((retry++))
    if [[ $retry -lt $max_retries ]]; then
      echo "${pfx}Push failed, retrying in 5 seconds... (attempt $((retry+1))/$max_retries)"
      sleep 5
    else
      echo "${pfx}Push failed after $max_retries attempts"
      set -euo pipefail
      return 1
    fi
  done
}

push_oci_pair() {
  local src="$1"
  local dst="$2"
  copy_retry "$src" "$dst" --preserve-digests
  local inspect_flags=(--tls-verify=false)
  if dest_no_creds; then
    inspect_flags+=(--no-creds)
  fi
  local want got
  want=$(skopeo inspect --format "{{.Digest}}" "$src")
  got=$(skopeo inspect "${inspect_flags[@]}" --format "{{.Digest}}" "$dst")
  if [[ "$want" != "$got" ]]; then
    echo "manifest digest changed: source $want dest $got ($src -> $dst)"
    return 1
  fi
}

push_archive_pair() {
  copy_retry "$1" "$2" --all
}

export -f dest_no_creds copy_retry push_oci_pair push_archive_pair

if tar -tf "$BUNDLE" | grep -qx 'oci/oci-layout'; then
  workdir="$(mktemp -d)"
  trap 'rm -rf "${workdir}"' EXIT
  tar -xf "$BUNDLE" -C "$workdir"
  refs_file="${workdir}/e2e-refs.tsv"
  if [ ! -f "${refs_file}" ]; then
    echo "OCI bundle missing e2e-refs.tsv"
    exit 1
  fi
  pairs_file="$(mktemp)"
  trap 'rm -rf "${workdir}"; rm -f "${pairs_file}"' EXIT
  while IFS=$'\t' read -r tag ref; do
    [ -n "${tag:-}" ] && [ -n "${ref:-}" ] || continue
    path="${ref#*/}"
    [[ "$path" == "$ref" ]] && path="${ref}"
    printf '%s %s\n' "oci:${workdir}/oci:${tag}" "docker://${REGISTRY_ENDPOINT}/${path}" >> "$pairs_file"
  done < "${refs_file}"
  count=$(wc -l < "$pairs_file")
  xargs -P "$JOBS" -n 2 bash -c 'push_oci_pair "$1" "$2"' _ < "$pairs_file"
  echo "Done. Pushed ${count} image(s) to ${REGISTRY_ENDPOINT}"
  exit 0
fi

mapfile -t REFS < <(tar -xOf "$BUNDLE" manifest.json | jq -r '.[].RepoTags[]')

pairs_file="$(mktemp)"
trap 'rm -f "${pairs_file}"' EXIT
for r in "${REFS[@]}"; do
  path="${r#*/}"
  [[ "$path" == "$r" ]] && path="${r}"
  printf '%s %s\n' "docker-archive:${BUNDLE}:${r}" "docker://${REGISTRY_ENDPOINT}/${path}" >> "$pairs_file"
done

xargs -P "$JOBS" -n 2 bash -c 'push_archive_pair "$1" "$2"' _ < "$pairs_file"
echo "Done. Pushed ${#REFS[@]} image(s) to ${REGISTRY_ENDPOINT}"

#!/usr/bin/env bash
set -euo pipefail

# Upload images from a bundle tar to a registry.
# Usage: ./upload-images.sh <bundle.tar> [--registry-endpoint host:port] [--jobs N]
#
# If REGISTRY_ENDPOINT is not provided, it will be calculated using registry_address()

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

echo "Pushing images from bundle: ${BUNDLE}"

mapfile -t REFS < <(tar -xOf "$BUNDLE" manifest.json | jq -r '.[].RepoTags[]')

pairs_file="$(mktemp)"
for r in "${REFS[@]}"; do
  path="${r#*/}"
  [[ "$path" == "$r" ]] && path="${r}"
  src="docker-archive:${BUNDLE}:${r}"
  dst="docker://${REGISTRY_ENDPOINT}/${path}"
  printf '%s %s\n' "$src" "$dst" >> "$pairs_file"
done

cat "$pairs_file" | xargs -P "$JOBS" -I{} bash -c '
  set -euo pipefail
  src=$(echo {} | awk "{print \$1}")
  dst=$(echo {} | awk "{print \$2}")
  tag="${dst##*:}"
  pfx="[push ${tag}] "
  echo "${pfx}${src} -> ${dst}"

  max_retries=3
  retry=0
  while [[ $retry -lt $max_retries ]]; do
    set +euo pipefail
    skopeo_output=$(skopeo copy --all --dest-tls-verify=false "$src" "$dst" 2>&1)
    skopeo_exit=$?
    echo "$skopeo_output" | awk -v p="$pfx" "{print p \$0}"

    if [[ $skopeo_exit -eq 0 ]]; then
      set -euo pipefail
      break
    fi

    ((retry++))
    if [[ $retry -lt $max_retries ]]; then
      echo "${pfx}Push failed, retrying in 5 seconds... (attempt $((retry+1))/$max_retries)"
      sleep 5
    else
      echo "${pfx}Push failed after $max_retries attempts"
      set -euo pipefail
      exit 1
    fi
  done
'

rm -f "$pairs_file"
echo "Done. Pushed ${#REFS[@]} image(s) to ${REGISTRY_ENDPOINT}"

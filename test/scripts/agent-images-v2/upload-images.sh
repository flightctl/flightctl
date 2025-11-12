#!/usr/bin/env bash
set -euo pipefail

# Usage: REGISTRY_ENDPOINT=host:port ./upload-archive-par.sh bundle.tar [--registry-endpoint host:port] [--insecure] [--jobs N]
BUNDLE="${1:?bundle tar required}"
shift || true

ARG_ENDPOINT=""
TLS_VERIFY=true
JOBS="${JOBS:-$(nproc)}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --registry-endpoint) ARG_ENDPOINT="$2"; shift 2 ;;
    --insecure) TLS_VERIFY=false; shift ;;
    --jobs) JOBS="$2"; shift 2 ;;
    *) echo "unknown arg: $1"; exit 2 ;;
  esac
done

REGISTRY_ENDPOINT="${REGISTRY_ENDPOINT:-$ARG_ENDPOINT}"
[[ -n "${REGISTRY_ENDPOINT}" ]] || { echo "set REGISTRY_ENDPOINT or pass --registry-endpoint"; exit 2; }

TLS_FLAG="--dest-tls-verify=${TLS_VERIFY}"

# Check registry availability
echo "Checking registry availability at ${REGISTRY_ENDPOINT}..."
CURL_ARGS="-f --connect-timeout 5"
[[ "${TLS_VERIFY}" == "false" ]] && CURL_ARGS+=" --insecure"

for i in {0..29}; do
  echo "Attempting connection... (${i}s/30s)"
  curl ${CURL_ARGS} "https://${REGISTRY_ENDPOINT}/v2/" 2>&1 && {
    echo "Registry is available"
    break
  }
  [[ $i -eq 29 ]] && { echo "Error: Registry unavailable after 30s"; exit 1; }
  sleep 1
done

# Read all RepoTags from the archive's manifest.json
# Each tag looks like: quay.io/flightctl/flightctl-device:v10-latest
mapfile -t REFS < <(tar -xOf "$BUNDLE" manifest.json | jq -r '.[].RepoTags[]')

# Prepare src,dst pairs for parallel push
# Strip the source registry and keep the path for destination
# Example: quay.io/flightctl/flightctl-device:v10-latest -> flightctl/flightctl-device:v10-latest
pairs_file="$(mktemp)"
for r in "${REFS[@]}"; do
  path="${r#*/}"                      # drop first path segment (registry)
  [[ "$path" == "$r" ]] && path="${r}"  # if no slash, keep as is
  src="docker-archive:${BUNDLE}:${r}"
  dst="docker://${REGISTRY_ENDPOINT}/${path}"
  printf '%s %s\n' "$src" "$dst" >> "$pairs_file"
done

# Push in parallel with readable logs and retry logic
cat "$pairs_file" | xargs -P "$JOBS" -I{} bash -c '
  set -euo pipefail
  src=$(echo {} | awk "{print \$1}")
  dst=$(echo {} | awk "{print \$2}")
  tag="${dst##*:}"
  pfx="[push ${tag}] "
  echo "${pfx}${src} -> ${dst}"

  # Retry up to 3 attempts with 5 second backoff
  max_retries=3
  retry=0
  while [[ $retry -lt $max_retries ]]; do
    set +euo pipefail
    skopeo_output=$(skopeo copy --all '"$TLS_FLAG"' "$src" "$dst" 2>&1)
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



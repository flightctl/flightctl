#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

API_URL="${1:?usage: run-provider-login-cypress.sh <api-url> [provider-name] [provider-ui] [username] [password] (prefer USERNAME/PASSWORD env vars for credentials)}"
PROVIDER_NAME="${2:-}"
PROVIDER_UI="${3:?provider-ui is required}"
USERNAME="${USERNAME:-${4:-}}"
PASSWORD="${PASSWORD:-${5:-}}"

if [[ -z "$USERNAME" ]]; then
  echo "username is required (set USERNAME or pass argv[4])" >&2
  exit 1
fi

if [[ -z "$PASSWORD" ]]; then
  echo "password is required (set PASSWORD or pass argv[5])" >&2
  exit 1
fi

FLIGHTCTL_BIN="${FLIGHTCTL:-flightctl}"
CALLBACK_PORT="${FLIGHTCTL_CALLBACK_PORT:-8080}"
LOG="$(mktemp)"
CYPRESS_BIN="./node_modules/.bin/cypress"

cleanup() {
  rm -f "$LOG"
}
trap cleanup EXIT

ensure_cypress_installed() {
  if [[ -x "$CYPRESS_BIN" ]]; then
    return 0
  fi

  if ! command -v npm >/dev/null 2>&1; then
    echo "Cypress is not installed in $SCRIPT_DIR and npm is not available to install it." >&2
    exit 1
  fi

  echo "Cypress is not installed in $SCRIPT_DIR. Installing dependencies automatically..." >&2
  npm install --no-fund --no-audit >&2

  if [[ ! -x "$CYPRESS_BIN" ]]; then
    echo "Cypress install completed, but $CYPRESS_BIN is still missing." >&2
    exit 1
  fi
}

ensure_cypress_installed


if command -v ss >/dev/null 2>&1; then
  PIDS="$(ss -ltnp "sport = :${CALLBACK_PORT}" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u)"
  if [[ -n "$PIDS" ]]; then
    echo "Killing stale process(es) on ${CALLBACK_PORT}: $PIDS" >&2
    while IFS= read -r pid; do
      [[ -n "$pid" ]] || continue
      kill "$pid" || true
    done <<< "$PIDS"
    sleep 1
  fi
fi

if [[ "$PROVIDER_UI" == "openshift" ]] && command -v oc >/dev/null 2>&1; then
  OPENSHIFT_OAUTH_CLIENT_ID="${OPENSHIFT_OAUTH_CLIENT_ID:-flightctl-flightctl}"
  echo "Ensuring OAuth client ${OPENSHIFT_OAUTH_CLIENT_ID} allows local callback..." >&2
  LOCALHOST_CALLBACK_URI="http://localhost:${CALLBACK_PORT}/callback"
  LOOPBACK_CALLBACK_URI="http://127.0.0.1:${CALLBACK_PORT}/callback"
  CURRENT_REDIRECT_URIS="$(oc get oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" -o jsonpath='{range .redirectURIs[*]}{.}{"\n"}{end}')"
  MISSING_REDIRECT_URIS=()

  for uri in "$LOCALHOST_CALLBACK_URI" "$LOOPBACK_CALLBACK_URI"; do
    if ! grep -Fxq "$uri" <<< "$CURRENT_REDIRECT_URIS"; then
      MISSING_REDIRECT_URIS+=("$uri")
    fi
  done

  if (( ${#MISSING_REDIRECT_URIS[@]} > 0 )); then
    if [[ -n "$CURRENT_REDIRECT_URIS" ]]; then
      PATCH_OPS=()
      for uri in "${MISSING_REDIRECT_URIS[@]}"; do
        PATCH_OPS+=("{\"op\":\"add\",\"path\":\"/redirectURIs/-\",\"value\":\"${uri}\"}")
      done
      PATCH_PAYLOAD="[$(IFS=,; echo "${PATCH_OPS[*]}")]"
      oc patch oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" --type=json -p "$PATCH_PAYLOAD" >/dev/null
    else
      PATCH_PAYLOAD="{\"redirectURIs\":[\"$LOCALHOST_CALLBACK_URI\",\"$LOOPBACK_CALLBACK_URI\"]}"
      oc patch oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" --type=merge -p "$PATCH_PAYLOAD" >/dev/null
    fi
  fi
fi

LOGIN_ARGS=(login "$API_URL" --web --no-browser --insecure-skip-tls-verify --callback-port "$CALLBACK_PORT")
if [[ -n "$PROVIDER_NAME" ]]; then
  LOGIN_ARGS+=(--provider "$PROVIDER_NAME")
fi

"$FLIGHTCTL_BIN" "${LOGIN_ARGS[@]}" >"$LOG" 2>&1 &
FLT_PID=$!

AUTHORIZE_URL=""
for _ in $(seq 1 240); do
  if grep -qE 'Please open this URL in your browser:|Opening login URL in default browser:' "$LOG" 2>/dev/null; then
    AUTHORIZE_URL="$(grep -E 'Please open this URL in your browser:|Opening login URL in default browser:' "$LOG" | sed -E 's/^.*browser:[[:space:]]*//' | tail -1 | tr -d '\r' || true)"
    if [[ -n "$AUTHORIZE_URL" ]]; then
      break
    fi
  fi

  sleep 0.5

  if ! kill -0 "$FLT_PID" 2>/dev/null; then
    echo "flightctl exited before printing an authorization URL. Output:" >&2
    cat "$LOG" >&2
    wait "$FLT_PID" || true
    exit 1
  fi
done

if [[ -z "$AUTHORIZE_URL" ]]; then
  echo "Timed out waiting for authorization URL in flightctl output." >&2
  cat "$LOG" >&2
  kill "$FLT_PID" 2>/dev/null || true
  wait "$FLT_PID" 2>/dev/null || true
  exit 1
fi

export CYPRESS_AUTHPROVIDER_AUTHORIZE_URL="$AUTHORIZE_URL"
export CYPRESS_AUTHPROVIDER_CALLBACK_PORT="$CALLBACK_PORT"
export CYPRESS_AUTHPROVIDER_UI="$PROVIDER_UI"
export CYPRESS_AUTHPROVIDER_USERNAME="$USERNAME"
export CYPRESS_AUTHPROVIDER_PASSWORD="$PASSWORD"

set +e
"$CYPRESS_BIN" run --config-file cypress.config.js --spec e2e/provider-login.cy.js
CYPRESS_EXIT=$?
set -e
if [[ "$CYPRESS_EXIT" -ne 0 ]]; then
  cat "$LOG" >&2 || true
  kill "$FLT_PID" 2>/dev/null || true
  wait "$FLT_PID" 2>/dev/null || true
  exit "$CYPRESS_EXIT"
fi

wait "$FLT_PID"

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
if [[ ! "$CALLBACK_PORT" =~ ^[0-9]+$ ]]; then
  echo "FLIGHTCTL_CALLBACK_PORT must contain only digits, got: ${CALLBACK_PORT}" >&2
  exit 1
fi
LOG="$(mktemp)"
CYPRESS_BIN="./node_modules/.bin/cypress"

cleanup() {
  rm -f "$LOG"
}
trap cleanup EXIT

# redact_credentials removes username/password-looking values from command output before logs are printed.
redact_credentials() {
  sed -E \
    -e 's/((USERNAME|PASSWORD|CYPRESS_AUTHPROVIDER_USERNAME|CYPRESS_AUTHPROVIDER_PASSWORD|authProviderUsername|authProviderPassword)[[:space:]]*[:=][[:space:]]*)("[^"]*"|'\''[^'\'']*'\''|[^[:space:]]+)/\1<REDACTED>/Ig' \
    -e 's/([[:space:]]-[up][[:space:]]+)[^[:space:]]+/\1<REDACTED>/g' \
    -e 's/(--(username|password)(=|[[:space:]]+))[^[:space:]]+/\1<REDACTED>/Ig'
}

# print_sanitized_log prints captured flightctl output without leaking test credentials.
print_sanitized_log() {
  redact_credentials < "$LOG" >&2 || true
}

# ensure_cypress_installed installs the suite-local Cypress dependencies when they are missing.
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

# resolve_openshift_oauth_client_id returns the single Flight Control OAuthClient name to patch.
resolve_openshift_oauth_client_id() {
  if [[ -n "${OPENSHIFT_OAUTH_CLIENT_ID:-}" ]]; then
    echo "$OPENSHIFT_OAUTH_CLIENT_ID"
    return 0
  fi

  local client_ids client_count
  client_ids="$(oc get oauthclient \
    -l flightctl.service=flightctl,component=oauth-client \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  client_count="$(grep -cve '^[[:space:]]*$' <<< "$client_ids" || true)"
  client_count="${client_count:-0}"

  case "$client_count" in
    0)
      echo "No Flight Control OAuthClient matches flightctl.service=flightctl,component=oauth-client; set OPENSHIFT_OAUTH_CLIENT_ID explicitly." >&2
      return 1
      ;;
    1)
      grep -ve '^[[:space:]]*$' <<< "$client_ids"
      ;;
    *)
      echo "Multiple Flight Control OAuthClients match flightctl.service=flightctl,component=oauth-client; set OPENSHIFT_OAUTH_CLIENT_ID explicitly." >&2
      printf '%s\n' "$client_ids" >&2
      return 1
      ;;
  esac
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
  OPENSHIFT_OAUTH_CLIENT_ID="$(resolve_openshift_oauth_client_id)"
  echo "Ensuring OAuth client ${OPENSHIFT_OAUTH_CLIENT_ID} allows local callback..." >&2
  LOCALHOST_CALLBACK_URI="http://localhost:${CALLBACK_PORT}/callback"
  LOOPBACK_CALLBACK_URI="http://127.0.0.1:${CALLBACK_PORT}/callback"
  if CURRENT_REDIRECT_URIS="$(oc get oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" -o jsonpath='{range .redirectURIs[*]}{.}{"\n"}{end}' 2>/dev/null)"; then
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
        if ! oc patch oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" --type=json -p "$PATCH_PAYLOAD" >/dev/null; then
          echo "Failed to patch OAuth client ${OPENSHIFT_OAUTH_CLIENT_ID} with callback redirectURIs." >&2
          exit 1
        fi
      else
        PATCH_PAYLOAD="{\"redirectURIs\":[\"$LOCALHOST_CALLBACK_URI\",\"$LOOPBACK_CALLBACK_URI\"]}"
        if ! oc patch oauthclient "$OPENSHIFT_OAUTH_CLIENT_ID" --type=merge -p "$PATCH_PAYLOAD" >/dev/null; then
          echo "Failed to initialize OAuth client ${OPENSHIFT_OAUTH_CLIENT_ID} callback redirectURIs." >&2
          exit 1
        fi
      fi
    fi
  else
    echo "Could not read OAuth client ${OPENSHIFT_OAUTH_CLIENT_ID}; continuing without patching redirectURIs." >&2
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
    print_sanitized_log
    wait "$FLT_PID" || true
    exit 1
  fi
done

if [[ -z "$AUTHORIZE_URL" ]]; then
  echo "Timed out waiting for authorization URL in flightctl output." >&2
  print_sanitized_log
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
"$CYPRESS_BIN" run --config-file cypress.config.js --spec e2e/provider-login.cy.js 2>&1 | redact_credentials
CYPRESS_EXIT=${PIPESTATUS[0]}
set -e
if [[ "$CYPRESS_EXIT" -ne 0 ]]; then
  print_sanitized_log
  kill "$FLT_PID" 2>/dev/null || true
  wait "$FLT_PID" 2>/dev/null || true
  exit "$CYPRESS_EXIT"
fi

wait "$FLT_PID"

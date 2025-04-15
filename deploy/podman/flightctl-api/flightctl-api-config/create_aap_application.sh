#!/usr/bin/env bash

set -eo pipefail

create_oauth_application() {
    local oauth_token="$1"
    local base_domain="$2"
    local aap_url="$3"
    local insecure_skip_tls="$4"

    # The Organization ID is hardcoded to 1 for now which is the 'Default' aap managed
    # organization that is created by default.
    json_payload=$(cat <<EOF
{
  "name": "Flight Control",
  "organization": 1,
  "authorization_grant_type": "authorization-code",
  "client_type": "public",
  "redirect_uris": "https://$base_domain:443/callback",
  "app_url": "https://$base_domain:443"
}
EOF
)

    echo "Creating OAuth Application"
    echo "OAuth Application Payload: $json_payload"

    # Use an array for curl options for better handling of spaces and special characters
    curl_opts=(-s)
    if [[ "$insecure_skip_tls" == "true" ]]; then
        echo "Warning: using insecure TLS connection"
        curl_opts+=(-k)
    fi
    if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
        echo "Using provided auth CA certificate"
        curl_opts+=(--cacert "$CERTS_SOURCE_PATH/auth/ca.crt")
    fi

    oauth_output_file=$(mktemp)
    http_status=$(curl "${curl_opts[@]}" -w "%{http_code}" -o "$oauth_output_file" -X POST "$aap_url/api/gateway/v1/applications/" \
        -H "Authorization: Bearer $oauth_token" \
        -H "Content-Type: application/json" \
        --data-raw "$json_payload")
    if [[ "$http_status" -lt 200 || "$http_status" -ge 300 ]]; then
        echo "Error: API call failed with status $http_status"
        cat "$oauth_output_file"
        rm "$oauth_output_file"
        exit 1
    fi

    echo "OAuth Application Result:"
    cat "$oauth_output_file"
    echo

    client_id=$(grep -oP '"client_id":\s*"\K[^"]+' "$oauth_output_file")
    if [[ -z "$client_id" || "$client_id" == "" ]]; then
        echo "Error: Failed to get client_id from response." >&2
        exit 1
    fi

    echo "Updating service config file with new client id: $client_id"
    # Tempfile here is necessary because we cannot directly modify the mounted config file in place
    tmpfile=$(mktemp)
    trap 'rm -f "$tmpfile" "$oauth_output_file"' EXIT
    if ! sed -E "s|^([[:space:]]*oAuthApplicationClientId:).*|\1 $client_id|" "$SERVICE_CONFIG_FILE" > "$tmpfile"; then
        echo "Error: Failed to update config file" >&2
        exit 1
    fi
    cat "$tmpfile" > "$SERVICE_CONFIG_FILE"
    echo "OAuth Application created successfully"
}

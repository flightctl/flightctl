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
  "redirect_uris": "https://$base_domain:443/callback http://127.0.0.1/callback",
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

    echo "Saving AAP OAuth client_id: $client_id"
    # Write client_id to /certs that is mounted from /etc/flightctl/pki on the host
    client_id_file="${CERTS_SOURCE_PATH}/aap-client-id"
    if echo "$client_id" > "$client_id_file"; then
        echo "AAP OAuth client_id saved to $client_id_file"
    else
        echo "Error: Failed to write client_id to $client_id_file" >&2
        exit 1
    fi

    rm -f "$oauth_output_file"
    echo "OAuth Application created successfully"
}

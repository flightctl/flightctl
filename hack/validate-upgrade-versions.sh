#!/usr/bin/env bash
set -euo pipefail

cli=${1:?Usage: validate-upgrade-versions.sh CLI_PATH}
expected_client=${EXPECTED_CLIENT_TAG:?Expected client tag is required}
expected_server=${EXPECTED_SERVER_TAG:?Expected server tag is required}

# Match version.Info.String(), preserving the real tree state of repaired builds.
case "${EXPECTED_SERVER_TREE_STATE:?Expected server tree state is required}" in
    clean) ;;
    dirty) expected_server+='-dirty' ;;
    *) echo "Unexpected server tree state: $EXPECTED_SERVER_TREE_STATE" >&2; exit 1 ;;
esac

versions=$("$cli" version)
client_version=$(printf '%s\n' "$versions" | awk '$1 == "Client" && $2 == "Version:" {print $3}')
server_version=$(printf '%s\n' "$versions" | awk '$1 == "Server" && $2 == "Version:" {print $3}')

[[ "$client_version" == "$expected_client" ]] || {
    echo "Client version mismatch: $client_version != $expected_client" >&2
    exit 1
}
[[ "$server_version" == "$expected_server" ]] || {
    echo "Server version mismatch: $server_version != $expected_server" >&2
    exit 1
}
echo "Versions validated: Client=$client_version, Server=$server_version"

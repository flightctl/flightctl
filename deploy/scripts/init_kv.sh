#!/usr/bin/env bash

set -eo pipefail

# Load secret generation functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/secrets.sh

main() {
    echo "Configuring KV secrets"

    ensure_kv_secrets

    echo "KV configuration complete"
}

main

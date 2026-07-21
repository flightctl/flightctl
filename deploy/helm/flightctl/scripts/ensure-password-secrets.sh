#!/bin/bash
set -euo pipefail

NAMESPACE=""
INTERNAL_NAMESPACE=""
ENSURE_DB_ADMIN="false"
ENSURE_DB_APP="false"
ENSURE_DB_MIGRATION="false"
ENSURE_KV="false"

KEEP_ANNOTATION="helm.sh/resource-policy=keep"
PRUNE_ANNOTATION="argocd.argoproj.io/sync-options=Prune=false"

usage() {
    cat <<EOF
Usage: $0 --namespace <ns> [options]

Ensures Flight Control password Secrets exist. Creates them only if missing;
never rotates existing password keys. Annotates Secrets so Helm/Argo CD do not
delete them when they leave the chart manifests.

Required:
  --namespace <ns>              Primary Kubernetes namespace

Optional:
  --internal-namespace <ns>     Also ensure Secrets in this namespace (same passwords)
  --ensure-db-admin             Ensure flightctl-db-admin-secret
  --ensure-db-app               Ensure flightctl-db-app-secret
  --ensure-db-migration         Ensure flightctl-db-migration-secret
  --ensure-kv                   Ensure flightctl-kv-secret
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --namespace)
            [[ $# -ge 2 ]] || { echo "Error: --namespace requires a value" >&2; usage; }
            NAMESPACE="$2"
            shift 2
            ;;
        --internal-namespace)
            [[ $# -ge 2 ]] || { echo "Error: --internal-namespace requires a value" >&2; usage; }
            INTERNAL_NAMESPACE="$2"
            shift 2
            ;;
        --ensure-db-admin)
            ENSURE_DB_ADMIN="true"
            shift
            ;;
        --ensure-db-app)
            ENSURE_DB_APP="true"
            shift
            ;;
        --ensure-db-migration)
            ENSURE_DB_MIGRATION="true"
            shift
            ;;
        --ensure-kv)
            ENSURE_KV="true"
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

if [[ -z "$NAMESPACE" ]]; then
    echo "Error: --namespace is required"
    usage
fi

if [[ "$ENSURE_DB_ADMIN" != "true" && "$ENSURE_DB_APP" != "true" && "$ENSURE_DB_MIGRATION" != "true" && "$ENSURE_KV" != "true" ]]; then
    echo "Error: at least one --ensure-* flag is required"
    usage
fi

if command -v oc &> /dev/null; then
    K8S_CLI="oc"
elif command -v kubectl &> /dev/null; then
    K8S_CLI="kubectl"
else
    echo "Error: Neither 'oc' nor 'kubectl' found in PATH"
    exit 1
fi

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

target_namespaces() {
    echo "$NAMESPACE"
    if [[ -n "$INTERNAL_NAMESPACE" && "$INTERNAL_NAMESPACE" != "$NAMESPACE" ]]; then
        echo "$INTERNAL_NAMESPACE"
    fi
}

generate_password() {
    local raw
    raw="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 20)"
    if [[ ${#raw} -ne 20 ]]; then
        echo "Error: failed to generate password" >&2
        exit 1
    fi
    printf '%s-%s-%s-%s' "${raw:0:5}" "${raw:5:5}" "${raw:10:5}" "${raw:15:5}"
}

secret_exists() {
    local ns="$1"
    local name="$2"
    local out
    if ! out=$("$K8S_CLI" get secret "$name" -n "$ns" --ignore-not-found -o name 2>&1); then
        echo "Error: failed to look up secret $name in namespace $ns" >&2
        echo "  Detail: $out" >&2
        exit 1
    fi
    [[ -n "$out" ]]
}

annotate_keep() {
    local ns="$1"
    local name="$2"
    "$K8S_CLI" annotate secret "$name" -n "$ns" \
        "$KEEP_ANNOTATION" \
        "$PRUNE_ANNOTATION" \
        --overwrite
}

get_data_key() {
    local ns="$1"
    local name="$2"
    local key="$3"
    "$K8S_CLI" get secret "$name" -n "$ns" -o "jsonpath={.data.${key}}"
}

find_secret_ns() {
    local name="$1"
    local ns
    for ns in $(target_namespaces); do
        if secret_exists "$ns" "$name"; then
            echo "$ns"
            return
        fi
    done
    echo ""
}

# Create secret from files; if it already exists, preserve and annotate (never overwrite data).
create_secret_from_files() {
    local ns="$1"
    local name="$2"
    shift 2

    if secret_exists "$ns" "$name"; then
        echo "Secret $name already exists in $ns — preserving data, applying keep annotations"
        annotate_keep "$ns" "$name"
        return
    fi

    echo "Creating secret $name in $ns"
    local out
    set +e
    out=$("$K8S_CLI" create secret generic "$name" --namespace="$ns" "$@" 2>&1)
    local rc=$?
    set -e
    if [[ $rc -ne 0 ]]; then
        if echo "$out" | grep -qiE 'already exists|AlreadyExists'; then
            echo "Secret $name appeared concurrently in $ns — preserving data, applying keep annotations"
            annotate_keep "$ns" "$name"
            return
        fi
        echo "Error: failed to create secret $name in $ns" >&2
        echo "  Detail: $out" >&2
        exit 1
    fi
    annotate_keep "$ns" "$name"
}

write_file() {
    local path="$1"
    local value="$2"
    umask 077
    printf '%s' "$value" > "$path"
    chmod 600 "$path"
}

preserve_or_create() {
    local name="$1"
    local password_key="$2"
    local source_ns
    local password=""
    local ns
    local dir

    source_ns="$(find_secret_ns "$name")"
    if [[ -n "$source_ns" ]]; then
        password="$(get_data_key "$source_ns" "$name" "$password_key")"
        if [[ -z "$password" ]]; then
            echo "Error: $name in $source_ns is missing data.$password_key" >&2
            exit 1
        fi
        password="$(printf '%s' "$password" | base64 -d)"
    else
        password="$(generate_password)"
    fi

    dir="$(mktemp -d "$WORKDIR/${name}.XXXXXX")"

    for ns in $(target_namespaces); do
        if secret_exists "$ns" "$name"; then
            echo "Secret $name already exists in $ns — preserving data, applying keep annotations"
            annotate_keep "$ns" "$name"
            continue
        fi
        case "$name" in
            flightctl-db-admin-secret)
                write_file "$dir/masterUser" "admin"
                write_file "$dir/masterPassword" "$password"
                create_secret_from_files "$ns" "$name" \
                    --from-file=masterUser="$dir/masterUser" \
                    --from-file=masterPassword="$dir/masterPassword"
                "$K8S_CLI" label secret "$name" -n "$ns" \
                    flightctl.service=flightctl-db-admin \
                    security.level=high-privilege \
                    --overwrite
                ;;
            flightctl-db-app-secret)
                write_file "$dir/user" "flightctl_app"
                write_file "$dir/userPassword" "$password"
                create_secret_from_files "$ns" "$name" \
                    --from-file=user="$dir/user" \
                    --from-file=userPassword="$dir/userPassword"
                "$K8S_CLI" label secret "$name" -n "$ns" \
                    flightctl.service=flightctl-db-app \
                    security.level=application \
                    --overwrite
                ;;
            flightctl-db-migration-secret)
                write_file "$dir/migrationUser" "flightctl_migrator"
                write_file "$dir/migrationPassword" "$password"
                create_secret_from_files "$ns" "$name" \
                    --from-file=migrationUser="$dir/migrationUser" \
                    --from-file=migrationPassword="$dir/migrationPassword"
                "$K8S_CLI" label secret "$name" -n "$ns" \
                    flightctl.service=flightctl-db-migration \
                    security.level=schema-privilege \
                    --overwrite
                ;;
            flightctl-kv-secret)
                write_file "$dir/password" "$password"
                create_secret_from_files "$ns" "$name" \
                    --from-file=password="$dir/password"
                "$K8S_CLI" label secret "$name" -n "$ns" \
                    flightctl.service=flightctl-kv \
                    --overwrite
                ;;
            *)
                echo "Error: unknown secret $name" >&2
                exit 1
                ;;
        esac
    done
}

echo "=== Ensuring Flight Control password Secrets ==="
echo "Using CLI: $K8S_CLI"
echo "Namespace: $NAMESPACE"
if [[ -n "$INTERNAL_NAMESPACE" && "$INTERNAL_NAMESPACE" != "$NAMESPACE" ]]; then
    echo "Internal namespace: $INTERNAL_NAMESPACE"
fi
echo ""

if [[ "$ENSURE_DB_ADMIN" == "true" ]]; then
    preserve_or_create "flightctl-db-admin-secret" "masterPassword"
fi
if [[ "$ENSURE_DB_APP" == "true" ]]; then
    preserve_or_create "flightctl-db-app-secret" "userPassword"
fi
if [[ "$ENSURE_DB_MIGRATION" == "true" ]]; then
    preserve_or_create "flightctl-db-migration-secret" "migrationPassword"
fi
if [[ "$ENSURE_KV" == "true" ]]; then
    preserve_or_create "flightctl-kv-secret" "password"
fi

echo ""
echo "=== Password Secret ensure complete ==="

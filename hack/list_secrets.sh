#!/bin/bash

set -euo pipefail

ORG="flightctl"
OLDER_THAN_DAYS=-1

usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Audits and lists GitHub Actions secrets across the '${ORG}' organization."
    echo ""
    echo "Options:"
    echo "  -h, --help               Show this help message and exit"
    echo "  --older-than <days>      Only show secrets older than the specified number of days"
    echo ""
    echo "Examples:"
    echo "  $0                       List all secrets with their age"
    echo "  $0 --older-than 4        List only secrets older than 4 days"
    exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      ;;
    --older-than)
      if [[ -z "${2:-}" ]] || ! [[ "$2" =~ ^[0-9]+$ ]]; then
        echo "Error: --older-than requires a non-negative integer argument." >&2
        exit 1
      fi
      OLDER_THAN_DAYS="$2"
      shift 2
      ;;
    *)
      echo "Unknown option $1" >&2
      usage
      ;;
  esac
done

NOW=$(date +%s)

printf "%-30s | %-45s | %-25s\n" "Repo" "Secret" "Last Modified"
echo "-------------------------------|-----------------------------------------------|--------------------------"

list_secrets() {
    local label=$1; shift
    if out=$(gh secret list "$@" 2>&1); then
        while IFS=$'\t' read -r name mod _; do
            if [[ -n "$name" && -n "$mod" ]]; then
                if [[ "$mod" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T ]]; then
                    # Support both macOS/BSD and GNU date
                    EPOCH=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$mod" +%s 2>/dev/null || date -d "$mod" +%s 2>/dev/null)
                    if [[ -n "$EPOCH" ]]; then
                        DAYS=$(( (NOW - EPOCH) / 86400 ))

                        # Filter out secrets newer than OLDER_THAN_DAYS if the flag is passed
                        if [[ "$OLDER_THAN_DAYS" -ge 0 && "$DAYS" -le "$OLDER_THAN_DAYS" ]]; then
                            continue
                        fi

                        mod="$mod ($DAYS days ago)"
                    fi
                fi
                printf "%-30s | %-45s | %-25s\n" "$label" "$name" "$mod"
            fi
        done <<< "$out"
    else
        printf "%-30s | %-73.73s\n" "$label" "ERROR: $(echo "$out" | head -n1)"
    fi
}

list_secrets "-- (org-level)" --org "$ORG"

# Capture the output of `gh repo list` to properly check for errors before iterating.
if ! REPOS=$(gh repo list "$ORG" --limit 1000 --json name -q '.[].name' 2>&1); then
    printf "%-30s | %-73.73s\n" "-- (repos)" "ERROR: $(echo "$REPOS" | head -n1)"
    exit 1
fi

for r in $(echo "$REPOS" | sort); do
    list_secrets "$r" -R "$ORG/$r"
done
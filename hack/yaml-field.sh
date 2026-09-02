#!/usr/bin/env bash
# yaml-field.sh - Extract a field value from a simple two-level YAML file.
# Usage: yaml-field.sh <file> <top-key> <field>
# Example: yaml-field.sh packaging/images/el10/images.yaml api build_base
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "Usage: yaml-field.sh <file> <top-key> <field>" >&2
  exit 1
fi

val=$(awk -v key="$2:" -v field="  $3:" '
  $0 == key || $0 == key " " { found=1; next }
  found && /^[^ #]/ { exit }
  found && index($0, field) == 1 {
    v = $0
    sub(/^[^:]*: */, "", v)
    gsub(/^["'"'"']|["'"'"']$/, "", v)
    print v
    exit
  }
' "$1")

# Exit nonzero if the field was not found.
if [[ -z "$val" ]]; then
  echo "ERROR: field '$3' not found under '$2' in $1" >&2
  exit 1
fi

# Validate: values must only contain safe image-ref characters
# (alphanumeric, dot, dash, underscore, slash, colon, at-sign).
if ! [[ "$val" =~ ^[a-zA-Z0-9._/:@-]+$ ]]; then
  echo "ERROR: unsafe value '$val' from $1 ($2.$3)" >&2
  exit 1
fi

printf '%s' "$val"

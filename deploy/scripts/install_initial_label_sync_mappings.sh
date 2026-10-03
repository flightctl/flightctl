#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <source manifest> <destination manifest>" >&2
  exit 2
fi

source_manifest=$1
destination_manifest=$2
destination_directory=$(dirname -- "$destination_manifest")

if [[ ! -f $source_manifest ]]; then
  echo "Initial LabelSyncMapping source is not a regular file: $source_manifest" >&2
  exit 1
fi

if [[ -e $destination_manifest || -L $destination_manifest ]]; then
  echo "Preserving existing initial LabelSyncMapping manifest: $destination_manifest"
  exit 0
fi

if [[ ! -d $destination_directory ]]; then
  install -d -m 0755 "$destination_directory"
fi

temporary_manifest=$(mktemp "${destination_manifest}.tmp.XXXXXX")
trap 'rm -f "$temporary_manifest"' EXIT
install -m 0644 "$source_manifest" "$temporary_manifest"

if ln "$temporary_manifest" "$destination_manifest" 2>/dev/null; then
  echo "Installed initial LabelSyncMapping manifest: $destination_manifest"
elif [[ -e $destination_manifest || -L $destination_manifest ]]; then
  echo "Preserving existing initial LabelSyncMapping manifest: $destination_manifest"
else
  echo "Failed to install initial LabelSyncMapping manifest: $destination_manifest" >&2
  exit 1
fi

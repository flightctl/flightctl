#!/bin/sh
# retry.sh — retry a command with exponential back-off.
# Usage: retry.sh <command> [args...]
#
# Retries up to 5 times with delays of 2, 4, 8, 16 seconds.
# Exits with the last command's exit code on final failure.
set -eu

MAX_ATTEMPTS=5
attempt=1
delay=2

while :; do
    if "$@"; then
        exit 0
    else
        status=$?
    fi

    if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
        printf 'retry.sh: command failed after %s attempts (exit %s): %s\n' \
            "$attempt" "$status" "$*" >&2
        exit "$status"
    fi

    printf 'retry.sh: attempt %s/%s failed (exit %s); retrying in %ss…\n' \
        "$attempt" "$MAX_ATTEMPTS" "$status" "$delay" >&2
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
done

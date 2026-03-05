#!/usr/bin/env bash
# Called by RESTler's token_refresh_command inside the container.
# Reads the auth token from the RESTLER_TOKEN env var set by the test script.
printf "{'app1': {}}\nAuthorization: Bearer %s\n" "$RESTLER_TOKEN"

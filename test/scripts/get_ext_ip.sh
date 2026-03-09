#!/usr/bin/env bash
if which ip 2>/dev/null 1>/dev/null; then
    # Support IPv6-only mode via environment variable
    if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
        # IPv6-only: use IPv6 route
        result=$(ip -6 route get 2001:4860:4860::8888 2>/dev/null | grep -oP 'src \K\S+' || true)
    else
        # Try IPv4 first, fall back to IPv6
        result=$(ip route get 1.1.1.1 2>/dev/null | grep -oP 'src \K\S+' || true)
        if [[ -z "$result" ]]; then
            result=$(ip -6 route get 2001:4860:4860::8888 2>/dev/null | grep -oP 'src \K\S+' || true)
        fi
    fi
    echo "$result"
else
    # MacOS does not have ip, so we use route and ifconfig instead
    if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
        # IPv6-only for MacOS
        INTERFACE=$(route get -inet6 2001:4860:4860::8888 2>/dev/null | grep interface | awk '{print $2}')
        ifconfig | grep $INTERFACE -A 10 | grep "inet6 " | grep -Fv "::1" | grep -Fv "fe80:" | awk '{print $2}' | head -n 1
    else
        INTERFACE=$(route get 1.1.1.1 | grep interface | awk '{print $2}')
        ifconfig | grep $INTERFACE -A 10 | grep "inet " | grep -Fv 127.0.0.1 | awk '{print $2}' | head -n 1
    fi
fi
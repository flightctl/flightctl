#!/usr/bin/env bash
# podman-net-rule.sh — add/remove an ephemeral ip rule for a Podman network.
# All Podman and ip operations run with sudo/root.
#
# Usage:
#   sudo ./podman-net-rule.sh add  <network> [priority]
#   sudo ./podman-net-rule.sh del  <network> [priority]
#   sudo ./podman-net-rule.sh show <network> [priority]
# Defaults: priority=100  (must be *smaller* than your VPN rule pref so it wins)
#
# Example:
#   sudo ./podman-net-rule.sh add flightctl 100
#   sudo ./podman-net-rule.sh show flightctl 100
#   sudo ./podman-net-rule.sh del flightctl 100

set -euo pipefail

cmd="${1:-}"
NET="${2:-}"
PREF="${3:-100}"

if [[ -z "$cmd" || -z "$NET" ]]; then
  echo "Usage: $0 {add|del|show} <podman-network> [priority]" >&2
  exit 1
fi

# Validate priority parameter
if ! [[ "$PREF" =~ ^[0-9]+$ ]] || [[ "$PREF" -lt 1 ]] || [[ "$PREF" -gt 32766 ]]; then
  echo "ERROR: Priority must be a number between 1 and 32766 (default: 100)" >&2
  exit 1
fi

# ---- helpers (jq-free JSON scraping via awk) ----

get_iface() {
  # Returns the bridge interface name (e.g., podman1) or empty if not found
  podman network inspect "$NET" 2>/dev/null \
    | awk -F\" '/"network_interface":/ {print $4; exit}'
}

get_subnets() {
  # Prints one CIDR per line for the given Podman network
  podman network inspect "$NET" 2>/dev/null \
    | awk -F\" '/"subnet":/ {print $4}'
}

network_exists() {
  # Returns 0 (true) if network exists, 1 (false) otherwise
  podman network inspect "$NET" >/dev/null 2>&1
}

list_networks() {
  # Lists all available Podman networks
  echo "Available networks:"
  podman network ls --format "{{.Name}}" | sed 's/^/  /'
}

check_priority_conflicts() {
  # Warn about existing rules at the same priority
  local existing_rules
  existing_rules="$(ip rule | grep -E "^[[:space:]]*$PREF:" || true)"
  if [[ -n "$existing_rules" ]]; then
    echo "WARN: Found existing rules at priority $PREF:" >&2
    echo "$existing_rules" | sed 's/^/  /' >&2
    echo >&2
  fi
}

validate_network() {
  # Check if network exists, if not show available networks and exit
  if ! network_exists; then
    echo "ERROR: Network '$NET' not found." >&2
    echo >&2
    list_networks >&2
    exit 1
  fi
}

# ---- operations ----

add_rules() {
  validate_network
  check_priority_conflicts
  
  local iface subnets changed=0
  iface="$(get_iface || true)"
  if [[ -z "$iface" ]]; then
    echo "ERROR: Could not read network '$NET' (missing or no permission?)." >&2
    exit 1
  fi

  echo "Network: $NET"
  echo "Bridge interface: $iface"
  echo "Rule priority (pref): $PREF"
  echo "Discovering subnets…"

  subnets="$(get_subnets || true)"
  if [[ -z "$subnets" ]]; then
    echo "ERROR: No subnets found for network '$NET'." >&2
    exit 1
  fi

  echo "$subnets" | sed 's/^/  subnet: /'

  while IFS= read -r cidr; do
    [[ -n "$cidr" ]] || continue
    # Escape dots in CIDR for regex matching
    local escaped_cidr="${cidr//./\\.}"
    if ip rule | grep -qE "^[[:space:]]*$PREF:[[:space:]].*to[[:space:]]+$escaped_cidr[[:space:]].*lookup[[:space:]]+main"; then
      echo "Rule already exists: pref $PREF to $cidr lookup main"
    else
      sudo ip rule add pref "$PREF" to "$cidr" lookup main
      echo "Added rule: pref $PREF to $cidr lookup main"
      changed=1
    fi
  done <<< "$subnets"

  if [[ $changed -eq 1 ]]; then
    sudo ip route flush cache
    echo "Flushed route cache."
  fi

  echo
  echo "Effective matching rules now:"
  ip rule | sed 's/^/  /' | grep -E "^[[:space:]]*$PREF:|^  0:" || true
}

del_rules() {
  # For deletion, warn if network doesn't exist but continue anyway for cleanup
  if ! network_exists; then
    echo "WARN: Network '$NET' not found; attempting deletion anyway for cleanup." >&2
    echo >&2
    list_networks >&2
    echo >&2
  fi
  
  local iface subnets changed=0
  iface="$(get_iface || true)"
  if [[ -z "$iface" ]]; then
    echo "WARN: Could not read network '$NET'; attempting deletion anyway." >&2
  else
    echo "Network: $NET"
    echo "Bridge interface: $iface"
    echo "Rule priority (pref): $PREF"
  fi

  subnets="$(get_subnets || true)"
  if [[ -z "$subnets" ]]; then
    echo "WARN: No subnets found for network '$NET'." >&2
    echo "Cannot safely delete rules without knowing the specific subnets." >&2
    echo "If you need to clean up rules manually, use:" >&2
    echo "  sudo ip rule list | grep 'pref $PREF'" >&2
    echo "  sudo ip rule del pref $PREF to <specific-cidr> lookup main" >&2
    return 1
  fi

  echo "$subnets" | sed 's/^/  subnet: /'

  while IFS= read -r cidr; do
    [[ -n "$cidr" ]] || continue
    # Escape dots in CIDR for regex matching
    local escaped_cidr="${cidr//./\\.}"
    if ip rule | grep -qE "^[[:space:]]*$PREF:[[:space:]].*to[[:space:]]+$escaped_cidr[[:space:]].*lookup[[:space:]]+main"; then
      sudo ip rule del pref "$PREF" to "$cidr" lookup main || true
      echo "Deleted rule: pref $PREF to $cidr lookup main"
      changed=1
    else
      echo "Not found: pref $PREF to $cidr lookup main"
    fi
  done <<< "$subnets"

  if [[ $changed -eq 1 ]]; then
    sudo ip route flush cache
    echo "Flushed route cache."
  fi

  echo
  echo "Remaining rules around this priority:"
  ip rule | sed 's/^/  /' | grep -E "^[[:space:]]*$PREF:|^  0:" || true
}

show_status() {
  validate_network
  
  local iface subnets
  iface="$(get_iface || true)"
  subnets="$(get_subnets || true)"

  echo "Network: $NET"
  echo "Bridge interface: ${iface:-unknown}"
  echo "Rule priority (pref): $PREF"
  echo "Subnets:"
  if [[ -n "$subnets" ]]; then
    echo "$subnets" | sed 's/^/  - /'
  else
    echo "  (none)"
  fi
  echo
  echo "Matching ip rules:"
  ip rule | sed 's/^/  /' | grep -E "^[[:space:]]*$PREF:|^  0:" || true

  if [[ -n "$subnets" ]]; then
    echo
    echo "Current routes that mention these subnets (all tables):"
    while IFS= read -r cidr; do
      [[ -n "$cidr" ]] || continue
      ip route show table all | grep -F "$cidr" | sed "s|^|  [$cidr] |" || true
    done <<< "$subnets"
  fi
}

case "$cmd" in
  add)  add_rules ;;
  del)  del_rules ;;
  show) show_status ;;
  *)    echo "Unknown command: $cmd" >&2; exit 1 ;;
esac


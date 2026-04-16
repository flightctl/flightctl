#!/usr/bin/env bash
set -euo pipefail

# Inject agent files into a qcow2 image using qemu-nbd.
# It resolves the active OSTree deployment etc so files appear at guest /etc.
# Optionally installs a registry CA for all TLS clients (podman, helm, etc.).
#
# Env or flags:
#   QCOW          - path to qcow2 (default: bin/output/qcow2/disk.qcow2)
#   AGENT_DIR     - local dir with config.yaml and certs/ (default: bin/agent/etc/flightctl)
#   MOUNT_DIR     - temporary mount point (default: /mnt/qcow)
#   REGISTRY_ADDRESS - host:port of your registry for CA install (default: auto-detected via registry_address)
#   E2E_CA        - path to CA certificate for your registry (default: bin/e2e-certs/pki/CA/ca.crt)
#   SOURCE_REPO   - remote repo prefix to remap (fixed: quay.io/flightctl)
#
# Usage:
#   ./inject_agent_files_into_qcow.sh [--qcow PATH] [--agent-dir PATH] [--mount-dir PATH] \
#                                     [--registry-address HOST:PORT] [--e2e-ca PATH]

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/functions

QCOW="${QCOW:-bin/output/qcow2/disk.qcow2}"
AGENT_DIR="${AGENT_DIR:-bin/agent/etc/flightctl}"
MOUNT_DIR="${MOUNT_DIR:-/mnt/qcow}"
REGISTRY_ADDRESS="${REGISTRY_ADDRESS:-}"
REGISTRY_HOSTNAME="${REGISTRY_HOSTNAME:-e2e-registry}"
E2E_CA="${E2E_CA:-bin/e2e-certs/pki/CA/ca.crt}"
SOURCE_REPO="quay.io/flightctl"

log()   { echo "[info] $*"; }
dbg()   { echo "[debug] $*"; }
fail()  { echo "ERROR: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --qcow) QCOW="$2"; shift 2 ;;
    --agent-dir) AGENT_DIR="$2"; shift 2 ;;
    --mount-dir) MOUNT_DIR="$2"; shift 2 ;;
    --registry-address) REGISTRY_ADDRESS="$2"; shift 2 ;;
    --e2e-ca) E2E_CA="$2"; shift 2 ;;
    -h|--help) sed -n '1,100p' "$0"; exit 0 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

[[ -f "$QCOW" ]] || fail "Missing qcow2: $QCOW"
[[ -d "$AGENT_DIR" ]] || fail "Missing dir: $AGENT_DIR"
[[ -f "$AGENT_DIR/config.yaml" ]] || fail "Missing $AGENT_DIR/config.yaml"

# Default REGISTRY_ADDRESS if not provided - use registry_address function for consistency
if [[ -z "$REGISTRY_ADDRESS" ]]; then
  REGISTRY_ADDRESS="$(registry_address)"
  [[ -n "$REGISTRY_ADDRESS" ]] || fail "Could not determine REGISTRY_ADDRESS using registry_address function"
fi

# Extract port from REGISTRY_ADDRESS with proper IPv6 handling
if [[ "$REGISTRY_ADDRESS" =~ ^\[.*\]:([0-9]+)$ ]]; then
    # Bracketed IPv6 with port: [fd00::1]:5000
    REGISTRY_PORT="${BASH_REMATCH[1]}"
elif [[ "$REGISTRY_ADDRESS" =~ :([0-9]+)$ ]]; then
    # IPv4/hostname with port: 192.168.1.1:5000 or hostname:5000
    REGISTRY_PORT="${BASH_REMATCH[1]}"
else
    # No port specified, use default
    REGISTRY_PORT="5000"
fi

# TLS certificate path must match the registry address used by VMs.
# In IPv6 mode, use hostname to avoid container tool parsing issues.
if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
  REG_TLS_HOSTPORT="${REGISTRY_HOSTNAME}:${REGISTRY_PORT}"
  log "IPv6 mode: VMs will use registry hostname: $REG_TLS_HOSTPORT"
else
  REG_TLS_HOSTPORT="$REGISTRY_ADDRESS"
  log "IPv4 mode: VMs will use registry IP: $REG_TLS_HOSTPORT"
fi

SOURCE_REPO="${SOURCE_REPO%/}"
if [[ "$SOURCE_REPO" != */* ]]; then
  fail "SOURCE_REPO must include registry and namespace (e.g. quay.io/flightctl)"
fi
SOURCE_REPO_PATH="${SOURCE_REPO#*/}"

log "QCOW=$QCOW"
log "AGENT_DIR=$AGENT_DIR"
log "MOUNT_DIR=$MOUNT_DIR"
log "REGISTRY_ADDRESS=$REGISTRY_ADDRESS"
log "REGISTRY_HOSTNAME=$REGISTRY_HOSTNAME"
log "REG_TLS_HOSTPORT=$REG_TLS_HOSTPORT"
log "IPV6_ONLY=${IPV6_ONLY:-false}"
log "E2E_CA=$E2E_CA"
log "SOURCE_REPO=$SOURCE_REPO"

sudo modprobe nbd max_part=16 || true

# Find a free /dev/nbdX
NBD_DEV=""
for dev in /dev/nbd{0..15}; do
  [[ -e "$dev" ]] || continue
  lsblk -no NAME "$dev" >/dev/null 2>&1 || continue
  sudo qemu-nbd --disconnect "$dev" >/dev/null 2>&1 || true
  if [[ $(lsblk -lno NAME "$dev" 2>/dev/null | wc -l || true) -le 1 ]]; then
    NBD_DEV="$dev"; break
  fi
done
[[ -n "$NBD_DEV" ]] || fail "No free /dev/nbdX device found"

cleanup() {
  set +e
  if mountpoint -q "$MOUNT_DIR"; then dbg "umount $MOUNT_DIR"; sudo umount "$MOUNT_DIR" || true; fi
  if [[ -n "$NBD_DEV" ]]; then dbg "qemu-nbd --disconnect $NBD_DEV"; sudo qemu-nbd --disconnect "$NBD_DEV" || true; fi
}
trap cleanup EXIT

log "Connecting $QCOW -> $NBD_DEV"
sudo qemu-nbd --connect "$NBD_DEV" "$QCOW"

# Pick a partition with a Linux fs, prefer the largest
sudo udevadm settle || true
PART=""
for i in {1..6}; do
  sudo partprobe "$NBD_DEV" >/dev/null 2>&1 || true
  sudo partx -u "$NBD_DEV"   >/dev/null 2>&1 || true
  sleep 1
  dbg "pass $i partitions:"
  lsblk -lnpo NAME,TYPE,FSTYPE,SIZE "$NBD_DEV" || true
  PART="$(lsblk -lnpo NAME,FSTYPE,SIZE "$NBD_DEV" | awk '/(ext4|xfs|btrfs)/{print $1, $3}' | sort -k2 -h | tail -1 | awk '{print $1}')"
  [[ -n "$PART" ]] && break
done
if [[ -z "$PART" ]]; then
  FS="$(sudo blkid -s TYPE -o value "$NBD_DEV" 2>/dev/null || true)"
  [[ "$FS" =~ ^(ext4|xfs|btrfs)$ ]] || fail "No valid filesystem on $NBD_DEV"
  PART="$NBD_DEV"; log "No partitions - using whole device as $FS"
fi

log "Mounting $PART at $MOUNT_DIR"
sudo mkdir -p "$MOUNT_DIR"
sudo mount "$PART" "$MOUNT_DIR"

# Resolve where guest /etc lives
DEPLOY_ETC=""
SYSROOT_ETC="$MOUNT_DIR/etc"

if [[ -d "$MOUNT_DIR/ostree" ]]; then
  log "ostree detected - resolving active deployment etc"
  # Follow boot symlink if present
  DEP_LINK="$(ls -1d "$MOUNT_DIR"/ostree/boot.*/*/*/0 2>/dev/null | head -n1 || true)"
  if [[ -n "$DEP_LINK" ]]; then
    DEPLOY_DIR="$(readlink -f "$DEP_LINK")"
    [[ -d "$DEPLOY_DIR/etc" ]] && DEPLOY_ETC="$DEPLOY_DIR/etc"
  fi
  # Fallback to newest deployment
  if [[ -z "$DEPLOY_ETC" && -d "$MOUNT_DIR/ostree/deploy" ]]; then
    CAND="$(ls -1dt "$MOUNT_DIR"/ostree/deploy/*/deploy/*.0 2>/dev/null | head -n1 || true)"
    [[ -d "$CAND/etc" ]] && DEPLOY_ETC="$CAND/etc"
  fi
  [[ -n "$DEPLOY_ETC" ]] || fail "Could not locate deployment etc under $MOUNT_DIR/ostree"
  log "DEPLOY_ETC=$DEPLOY_ETC"
else
  log "non bootc layout - using plain /etc"
  DEPLOY_ETC="$MOUNT_DIR/etc"
fi

copy_into() {
  local base="$1"
  log "Copying into $base/flightctl"
  sudo install -d "$base/flightctl/certs"
  sudo install -m 0644 "$AGENT_DIR/config.yaml" "$base/flightctl/config.yaml"
  if compgen -G "$AGENT_DIR/certs/*" >/dev/null; then
    sudo cp -a "$AGENT_DIR"/certs/. "$base/flightctl/certs/"
  fi
  if compgen -G "$base/flightctl/certs/*.key" >/dev/null; then
    sudo chmod 600 "$base/flightctl/certs/"*.key || true
  fi
  sudo chown -R root:root "$base/flightctl"
  sudo sh -c "echo 'injected $(date -u +%FT%TZ)' > '$base/flightctl/INJECTION_OK'"
  dbg "tree:"
  sudo ls -l "$base/flightctl" || true
  sudo ls -l "$base/flightctl/certs" || true
}

# Install the E2E registry CA into the system trust anchors.
# This location persists across bootc OS updates via 3-way merge.
#
# All TLS clients (podman, skopeo, helm, curl, etc.) use the system CA bundle
# at /etc/pki/tls/certs/ca-bundle.crt. This bundle is regenerated from anchors
# only when update-ca-trust runs. Since we're injecting into a disk image (not
# a running system), we install a oneshot service to run update-ca-trust on boot
# before networking starts. This ensures the CA is trusted for all tools without
# needing per-tool drop-in configurations.
inject_registry_ca() {
  local base="$1"

  if [[ ! -f "$E2E_CA" ]]; then
    log "E2E CA not found at $E2E_CA - skipping"
    return
  fi

  local target_dir="$base/containers/certs.d/$REG_TLS_HOSTPORT"
  log "Installing E2E CA to $target_dir/ca.crt"
  sudo install -d "$target_dir"
  sudo install -m 0644 "$E2E_CA" "$target_dir/ca.crt"
  sudo chown -R root:root "$base/containers"

  local anchors_dir="$base/pki/ca-trust/source/anchors"
  log "Installing E2E CA to $anchors_dir/"
  sudo install -d "$anchors_dir"
  sudo install -m 0644 "$E2E_CA" "$anchors_dir/flightctl-e2e-registry.crt"

  local systemd_dir="$base/systemd/system"
  sudo install -d "$systemd_dir" "$systemd_dir/multi-user.target.wants"
  sudo tee "$systemd_dir/flightctl-update-ca-trust.service" >/dev/null <<'UNIT'
[Unit]
Description=Update CA trust for flightctl registry
ConditionPathExists=/etc/pki/ca-trust/source/anchors/flightctl-e2e-registry.crt
DefaultDependencies=no
Before=network-pre.target flightctl-agent.service
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/usr/bin/update-ca-trust
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
  sudo ln -sf "/etc/systemd/system/flightctl-update-ca-trust.service" \
    "$systemd_dir/multi-user.target.wants/flightctl-update-ca-trust.service"
}

write_registry_remap() {
  local base="$1"
  local config_dir="$base/containers/registries.conf.d"
  local remap_file="$config_dir/flightctl-remap.conf"
  local dest="${REG_TLS_HOSTPORT}/${SOURCE_REPO_PATH}"
  # Private registry is on port 5002 (same host, different port)
  local private_host="${REG_TLS_HOSTPORT%:*}"
  local private_dest="${private_host}:5002/${SOURCE_REPO_PATH}"
  log "Configuring registry remap $remap_file ($SOURCE_REPO -> $dest)"
  log "Configuring registry remap $remap_file (${SOURCE_REPO}-private -> $private_dest)"
  sudo install -d "$config_dir"
  sudo tee "$remap_file" >/dev/null <<EOF
[[registry]]
prefix = "${SOURCE_REPO}"
location = "${dest}"

[[registry]]
prefix = "${SOURCE_REPO}-private"
location = "${private_dest}"
EOF
  sudo chown root:root "$remap_file"
}

# Inject /etc/hosts entry so VM can resolve the host's hostname
inject_hosts_entry() {
  local base="$1"
  local hosts_file="$base/hosts"

  # Get host IP and hostname
  local host_ip
  host_ip=$(get_ext_ip)
  local host_fqdn
  host_fqdn=$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo "")
  local host_short
  host_short=$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo "")

  if [[ -z "$host_ip" ]]; then
    log "Could not determine host IP - skipping /etc/hosts injection"
    return
  fi

  # Add host FQDN entry only if hostname is valid (not localhost or empty)
  if [[ -n "$host_fqdn" ]] && [[ "$host_fqdn" != "localhost" ]] && [[ "$host_fqdn" != "localhost.localdomain" ]]; then
    log "Adding hosts entry: $host_ip -> $host_fqdn $host_short"

    if [[ -f "$hosts_file" ]]; then
      if ! sudo grep -qF -- "$host_fqdn" "$hosts_file" 2>/dev/null; then
        echo "$host_ip $host_fqdn $host_short" | sudo tee -a "$hosts_file" >/dev/null
      else
        log "Hosts entry for $host_fqdn already exists"
      fi
    else
      sudo tee "$hosts_file" >/dev/null <<EOF
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
$host_ip $host_fqdn $host_short
EOF
    fi
  else
    log "Host FQDN is localhost or empty - skipping host FQDN entry"
    # Ensure minimal hosts file exists for registry entry below
    if [[ ! -f "$hosts_file" ]]; then
      sudo tee "$hosts_file" >/dev/null <<EOF
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
EOF
    fi
  fi

  # Add registry hostname entry for IPv6 mode
  if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
    if ! sudo grep -qF -- "$REGISTRY_HOSTNAME" "$hosts_file" 2>/dev/null; then
      log "IPv6 mode: Adding hosts entry $host_ip -> $REGISTRY_HOSTNAME"
      echo "$host_ip $REGISTRY_HOSTNAME" | sudo tee -a "$hosts_file" >/dev/null
    else
      log "IPv6 mode: Registry hostname entry already exists"
    fi
  fi

  sudo chown root:root "$hosts_file"
  log "Hosts entry added successfully"
}

# Write to deployment etc so it appears at guest /etc
copy_into "$DEPLOY_ETC"
inject_registry_ca "$DEPLOY_ETC"
write_registry_remap "$DEPLOY_ETC"
inject_hosts_entry "$DEPLOY_ETC"

# Also mirror to on-disk etc so it shows under guest /sysroot/etc
if [[ "$SYSROOT_ETC" != "$DEPLOY_ETC" ]]; then
  copy_into "$SYSROOT_ETC"
  inject_registry_ca "$SYSROOT_ETC"
  write_registry_remap "$SYSROOT_ETC"
  inject_hosts_entry "$SYSROOT_ETC"
fi

sync
log "done"
# cleanup by trap

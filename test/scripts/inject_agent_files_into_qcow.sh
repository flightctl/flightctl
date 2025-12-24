#!/usr/bin/env bash
set -euo pipefail

# Inject agent files into a qcow2 image using qemu-nbd.
# It resolves the active OSTree deployment etc so files appear at guest /etc.
# Optionally installs a registry CA for podman/skopeo.
#
# Env or flags:
#   QCOW          - path to qcow2 (default: bin/output/qcow2/disk.qcow2)
#   AGENT_DIR     - local dir with config.yaml and certs/ (default: bin/agent/etc/flightctl)
#   MOUNT_DIR     - temporary mount point (default: /mnt/qcow)
#   REGISTRY_ADDR - host:port of your registry for CA install (default: <host LAN IP>:5000)
#   E2E_CA        - path to CA certificate for your registry (default: bin/e2e-certs/pki/CA/ca.crt)
#   SOURCE_REPO   - remote repo prefix to remap (fixed: quay.io/flightctl)
#
# Usage:
#   ./inject_agent_files_into_qcow.sh [--qcow PATH] [--agent-dir PATH] [--mount-dir PATH] \
#                                     [--registry-addr HOST:PORT] [--e2e-ca PATH]

QCOW="${QCOW:-bin/output/qcow2/disk.qcow2}"
AGENT_DIR="${AGENT_DIR:-bin/agent/etc/flightctl}"
MOUNT_DIR="${MOUNT_DIR:-/mnt/qcow}"
REGISTRY_ADDR="${REGISTRY_ADDR:-}"
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
    --registry-addr) REGISTRY_ADDR="$2"; shift 2 ;;
    --e2e-ca) E2E_CA="$2"; shift 2 ;;
    -h|--help) sed -n '1,100p' "$0"; exit 0 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

[[ -f "$QCOW" ]] || fail "Missing qcow2: $QCOW"
[[ -d "$AGENT_DIR" ]] || fail "Missing dir: $AGENT_DIR"
[[ -f "$AGENT_DIR/config.yaml" ]] || fail "Missing $AGENT_DIR/config.yaml"

# Default REGISTRY_ADDR if not provided
if [[ -z "$REGISTRY_ADDR" ]]; then
  if command -v ip >/dev/null 2>&1; then
    HOST_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '/src/ {for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')"
  else
    HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
  fi
  [[ -n "${HOST_IP:-}" ]] || fail "Could not determine host IP for default REGISTRY_ADDR"
  REGISTRY_ADDR="${HOST_IP}:5000"
fi

# CA install directory must match the host:port of the registry
REG_TLS_HOSTPORT="$REGISTRY_ADDR"

SOURCE_REPO="${SOURCE_REPO%/}"
if [[ "$SOURCE_REPO" != */* ]]; then
  fail "SOURCE_REPO must include registry and namespace (e.g. quay.io/flightctl)"
fi
SOURCE_REPO_PATH="${SOURCE_REPO#*/}"

log "QCOW=$QCOW"
log "AGENT_DIR=$AGENT_DIR"
log "MOUNT_DIR=$MOUNT_DIR"
log "REGISTRY_ADDR=$REGISTRY_ADDR"
log "E2E_CA=$E2E_CA"
log "REG_TLS_HOSTPORT=$REG_TLS_HOSTPORT"
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

# Inject registry CA into containers trust for podman and skopeo
inject_registry_ca() {
  local base="$1"
  local target_dir="$base/containers/certs.d/$REG_TLS_HOSTPORT"
  if [[ -f "$E2E_CA" ]]; then
    log "Installing E2E CA to $target_dir/ca.crt"
    sudo install -d "$target_dir"
    sudo install -m 0644 "$E2E_CA" "$target_dir/ca.crt"
    sudo chown -R root:root "$base/containers"
  else
    log "E2E CA not found at $E2E_CA - skipping registry CA injection"
  fi
}

write_registry_remap() {
  local base="$1"
  local config_dir="$base/containers/registries.conf.d"
  local remap_file="$config_dir/flightctl-remap.conf"
  local dest="${REG_TLS_HOSTPORT}/${SOURCE_REPO_PATH}"
  log "Configuring registry remap $remap_file ($SOURCE_REPO -> $dest)"
  sudo install -d "$config_dir"
  sudo tee "$remap_file" >/dev/null <<EOF
[[registry]]
prefix = "${SOURCE_REPO}"
location = "${dest}"
EOF
  sudo chown root:root "$remap_file"
}

# Write to deployment etc so it appears at guest /etc
copy_into "$DEPLOY_ETC"
inject_registry_ca "$DEPLOY_ETC"
write_registry_remap "$DEPLOY_ETC"

# Also mirror to on-disk etc so it shows under guest /sysroot/etc
if [[ "$SYSROOT_ETC" != "$DEPLOY_ETC" ]]; then
  copy_into "$SYSROOT_ETC"
  inject_registry_ca "$SYSROOT_ETC"
  write_registry_remap "$SYSROOT_ETC"
fi

sync
log "done"
# cleanup by trap

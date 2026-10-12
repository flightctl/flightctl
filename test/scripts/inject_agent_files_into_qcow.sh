#!/usr/bin/env bash
set -euo pipefail

# Inject agent files into a qcow2 image.
# Root runs retain the qemu-nbd path. Unprivileged runs use virt-customize and
# resolve the active OSTree deployment from inside the guest filesystem.
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
MIRROR_REGISTRY="${MIRROR_REGISTRY:-}"

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
# quay.io/flightctl-tests holds fixture images referenced directly by e2e specs
# (not built locally, so they don't go through SOURCE_REPO's bundle upload). Remap
# it the same way so devices pull from the local mirror (see
# MirrorExternalTestImages) instead of the real quay.io on every fresh VM.
TESTS_SOURCE_REPO="quay.io/flightctl-tests"
TESTS_SOURCE_REPO_PATH="${TESTS_SOURCE_REPO#*/}"

# Auto-detect mirror registry from OCP ImageTagMirrorSet if not explicitly set
if [[ -z "$MIRROR_REGISTRY" ]] && command -v oc &>/dev/null; then
  MIRROR_REGISTRY=$(oc get imagetagmirrorset -o \
    jsonpath='{range .items[*].spec.imageTagMirrors[*]}{.source}{" "}{.mirrors[0]}{"\n"}{end}' 2>/dev/null \
    | awk '$1 == "quay.io" || index($1, "quay.io/") == 1 {print $2; exit}') || true
fi

log "QCOW=$QCOW"
log "AGENT_DIR=$AGENT_DIR"
log "MOUNT_DIR=$MOUNT_DIR"
log "REGISTRY_ADDRESS=$REGISTRY_ADDRESS"
log "REGISTRY_HOSTNAME=$REGISTRY_HOSTNAME"
log "REG_TLS_HOSTPORT=$REG_TLS_HOSTPORT"
log "IPV6_ONLY=${IPV6_ONLY:-false}"
log "E2E_CA=$E2E_CA"
log "SOURCE_REPO=$SOURCE_REPO"
log "MIRROR_REGISTRY=${MIRROR_REGISTRY:-<not set>}"

inject_with_libguestfs() {
  command -v virt-customize >/dev/null 2>&1 || fail "Unprivileged qcow2 injection requires libguestfs virt-customize. Install it on the host and retry."

  local qcow_abs agent_dir_abs ca_abs host_ip host_fqdn host_short
  qcow_abs="$(readlink -f "$QCOW")"
  agent_dir_abs="$(cd "$AGENT_DIR" && pwd)"
  ca_abs=""
  if [[ -f "$E2E_CA" ]]; then
    ca_abs="$(readlink -f "$E2E_CA")"
  fi

  host_ip="$(get_ext_ip)"
  host_fqdn="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo "")"
  host_short="$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo "")"

  local staging_dir payload_dir payload_tar guest_script vars_file remote_input_dir
  staging_dir="$(mktemp -d "${TMPDIR:-/tmp}/flightctl-qcow-inject.XXXXXX")"
  ROOTLESS_STAGING_DIR="${staging_dir}"
  trap 'rm -rf -- "${ROOTLESS_STAGING_DIR}"' EXIT
  payload_dir="${staging_dir}/payload"
  mkdir -p "${payload_dir}/agent"
  install -m 0644 "${agent_dir_abs}/config.yaml" "${payload_dir}/agent/config.yaml"
  if [[ -d "${agent_dir_abs}/certs" ]]; then
    cp -a "${agent_dir_abs}/certs" "${payload_dir}/agent/certs"
  fi
  if [[ -n "${ca_abs}" ]]; then
    install -m 0644 "${ca_abs}" "${payload_dir}/e2e-ca.crt"
  fi

  remote_input_dir="/tmp/flightctl-qcow-inject-$(basename "${staging_dir}")"
  vars_file="${payload_dir}/vars.sh"
  : > "${vars_file}"
  write_guest_var() {
    printf '%s=%q\n' "$1" "$2" >> "${vars_file}"
  }
  write_guest_var INPUT_DIR "${remote_input_dir}"
  write_guest_var REG_TLS_HOSTPORT "${REG_TLS_HOSTPORT}"
  write_guest_var REGISTRY_HOSTNAME "${REGISTRY_HOSTNAME}"
  write_guest_var SOURCE_REPO_PATH "${SOURCE_REPO_PATH}"
  write_guest_var TESTS_SOURCE_REPO_PATH "${TESTS_SOURCE_REPO_PATH}"
  write_guest_var MIRROR_REGISTRY "${MIRROR_REGISTRY}"
  write_guest_var HOST_IP "${host_ip}"
  write_guest_var HOST_FQDN "${host_fqdn}"
  write_guest_var HOST_SHORT "${host_short}"
  write_guest_var QUADLET_HOST "${QUADLET_HOST:-}"
  write_guest_var IPV6_ONLY "${IPV6_ONLY:-false}"
  payload_tar="${staging_dir}/payload.tar"
  tar -C "${payload_dir}" -cf "${payload_tar}" .

  guest_script="${staging_dir}/inject-guest.sh"
  {
  cat <<'GUEST_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
GUEST_SCRIPT
  printf 'INPUT_DIR=%q\n' "${remote_input_dir}"
  cat <<'GUEST_SCRIPT'

source "$INPUT_DIR/vars.sh"
trap 'rm -rf -- "$INPUT_DIR"' EXIT

log() { echo "[guest-info] $*"; }
SOURCE_REPO="quay.io/flightctl"
TESTS_SOURCE_REPO="quay.io/flightctl-tests"
SYSROOT_ETC="/etc"
DEPLOY_ETC=""

if [[ -d /ostree ]]; then
  log "ostree detected - resolving active deployment etc"
  DEP_LINK="$(ls -1d /ostree/boot.*/*/*/0 2>/dev/null | head -n1 || true)"
  if [[ -n "${DEP_LINK}" ]]; then
    DEPLOY_DIR="$(readlink -f "${DEP_LINK}")"
    [[ -d "${DEPLOY_DIR}/etc" ]] && DEPLOY_ETC="${DEPLOY_DIR}/etc"
  fi
  if [[ -z "${DEPLOY_ETC}" && -d /ostree/deploy ]]; then
    CAND="$(ls -1dt /ostree/deploy/*/deploy/*.0 2>/dev/null | head -n1 || true)"
    [[ -d "${CAND}/etc" ]] && DEPLOY_ETC="${CAND}/etc"
  fi
  [[ -n "${DEPLOY_ETC}" ]] || { echo "Could not locate deployment etc under /ostree" >&2; exit 1; }
else
  log "non bootc layout - using plain /etc"
  DEPLOY_ETC="/etc"
fi

copy_into() {
  local base="$1"
  log "Copying into ${base}/flightctl"
  install -d "${base}/flightctl/certs"
  install -m 0644 "${INPUT_DIR}/agent/config.yaml" "${base}/flightctl/config.yaml"
  if [[ -d "${INPUT_DIR}/agent/certs" ]]; then
    cp -a "${INPUT_DIR}/agent/certs/." "${base}/flightctl/certs/"
  fi
  shopt -s nullglob
  local key_files=("${base}/flightctl/certs/"*.key)
  if [[ "${#key_files[@]}" -gt 0 ]]; then
    chmod 0600 "${key_files[@]}"
  fi
  chown -R 0:0 "${base}/flightctl"
  printf 'injected %s\n' "$(date -u +%FT%TZ)" > "${base}/flightctl/INJECTION_OK"
}

inject_registry_ca() {
  local base="$1"
  [[ -f "${INPUT_DIR}/e2e-ca.crt" ]] || { log "E2E CA not provided - skipping"; return; }

  local target_dir="${base}/containers/certs.d/${REG_TLS_HOSTPORT}"
  log "Installing E2E CA to ${target_dir}/ca.crt"
  install -d "${target_dir}"
  install -m 0644 "${INPUT_DIR}/e2e-ca.crt" "${target_dir}/ca.crt"
  chown -R 0:0 "${base}/containers"

  local anchors_dir="${base}/pki/ca-trust/source/anchors"
  install -d "${anchors_dir}"
  install -m 0644 "${INPUT_DIR}/e2e-ca.crt" "${anchors_dir}/flightctl-e2e-registry.crt"

  local systemd_dir="${base}/systemd/system"
  mkdir -p "${systemd_dir}/multi-user.target.wants"
  chown 0:0 "${systemd_dir}" "${systemd_dir}/multi-user.target.wants"
  chmod 0755 "${systemd_dir}" "${systemd_dir}/multi-user.target.wants"
  cat > "${systemd_dir}/flightctl-update-ca-trust.service" <<'UNIT'
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
  ln -sf "/etc/systemd/system/flightctl-update-ca-trust.service" \
    "${systemd_dir}/multi-user.target.wants/flightctl-update-ca-trust.service"
}

write_registry_remap() {
  local base="$1"
  local config_dir="${base}/containers/registries.conf.d"
  local remap_file="${config_dir}/flightctl-remap.conf"
  local dest="${REG_TLS_HOSTPORT}/${SOURCE_REPO_PATH}"
  local private_host="${REG_TLS_HOSTPORT%:*}"
  local private_dest="${private_host}:5002/${SOURCE_REPO_PATH}"
  local tests_dest="${REG_TLS_HOSTPORT}/${TESTS_SOURCE_REPO_PATH}"
  log "Configuring local registry remaps under ${config_dir}"
  install -d "${config_dir}"
  cat > "${remap_file}" <<EOF
[[registry]]
prefix = "${SOURCE_REPO}"
location = "${dest}"

[[registry]]
prefix = "${SOURCE_REPO}-private"
location = "${private_dest}"

[[registry]]
prefix = "${TESTS_SOURCE_REPO}"
location = "${tests_dest}"
EOF
  chown 0:0 "${remap_file}"
}

write_mirror_registry() {
  local base="$1"
  [[ -n "${MIRROR_REGISTRY}" ]] || { log "No mirror registry configured - skipping"; return; }

  local config_dir="${base}/containers/registries.conf.d"
  local mirror_file="${config_dir}/100-mirror-registry.conf"
  log "Configuring mirror registry remap: quay.io -> ${MIRROR_REGISTRY}"
  install -d "${config_dir}"
  cat > "${mirror_file}" <<EOF
[[registry]]
prefix = "quay.io:443"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "quay.io"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "registry.redhat.io"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "registry.access.redhat.com"
location = "${MIRROR_REGISTRY}"
insecure = true
EOF
  chown 0:0 "${mirror_file}"

  local policy_file="${base}/containers/policy.json"
  cat > "${policy_file}" <<EOF
{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {
    "docker": {
      "${MIRROR_REGISTRY}": [{"type": "insecureAcceptAnything"}]
    },
    "docker-daemon": {
      "": [{"type": "insecureAcceptAnything"}]
    }
  }
}
EOF
  chown 0:0 "${policy_file}"
}

inject_hosts_entry() {
  local base="$1"
  local hosts_file="${base}/hosts"
  [[ -n "${HOST_IP}" ]] || { log "Could not determine host IP - skipping /etc/hosts injection"; return; }

  if [[ -n "${HOST_FQDN}" && "${HOST_FQDN}" != "localhost" && "${HOST_FQDN}" != "localhost.localdomain" ]]; then
    log "Adding hosts entry: ${HOST_IP} -> ${HOST_FQDN} ${HOST_SHORT}"
    if [[ -f "${hosts_file}" ]]; then
      if ! grep -qF -- "${HOST_FQDN}" "${hosts_file}" 2>/dev/null; then
        printf '%s %s %s\n' "${HOST_IP}" "${HOST_FQDN}" "${HOST_SHORT}" >> "${hosts_file}"
      fi
    else
      cat > "${hosts_file}" <<EOF
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
${HOST_IP} ${HOST_FQDN} ${HOST_SHORT}
EOF
    fi
  else
    log "Host FQDN is localhost or empty - skipping host FQDN entry"
    if [[ ! -f "${hosts_file}" ]]; then
      cat > "${hosts_file}" <<'HOSTS'
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
HOSTS
    fi
  fi

  if [[ -n "${QUADLET_HOST}" && "${QUADLET_HOST}" != "localhost" ]] && \
      ! grep -qF -- "flightctl-vm.local" "${hosts_file}" 2>/dev/null; then
    log "Adding FlightCtl VM hosts entry: ${QUADLET_HOST} -> flightctl-vm.local"
    printf '%s flightctl-vm.local\n' "${QUADLET_HOST}" >> "${hosts_file}"
  fi

  if [[ "${IPV6_ONLY}" == "true" ]] && ! grep -qF -- "${REGISTRY_HOSTNAME}" "${hosts_file}" 2>/dev/null; then
    log "IPv6 mode: Adding hosts entry ${HOST_IP} -> ${REGISTRY_HOSTNAME}"
    printf '%s %s\n' "${HOST_IP}" "${REGISTRY_HOSTNAME}" >> "${hosts_file}"
  fi

  chown 0:0 "${hosts_file}"
  log "Hosts entry added successfully"
}

copy_into "${DEPLOY_ETC}"
inject_registry_ca "${DEPLOY_ETC}"
write_registry_remap "${DEPLOY_ETC}"
write_mirror_registry "${DEPLOY_ETC}"
inject_hosts_entry "${DEPLOY_ETC}"

if [[ "${SYSROOT_ETC}" != "${DEPLOY_ETC}" ]]; then
  copy_into "${SYSROOT_ETC}"
  inject_registry_ca "${SYSROOT_ETC}"
  write_registry_remap "${SYSROOT_ETC}"
  write_mirror_registry "${SYSROOT_ETC}"
  inject_hosts_entry "${SYSROOT_ETC}"
fi

sync
log "done"
GUEST_SCRIPT
  } > "${guest_script}"
  chmod 0755 "${guest_script}"

  log "Using unprivileged libguestfs injection; no host NBD device or mount is required"
  virt-customize --format qcow2 --add "${qcow_abs}" --no-network \
    --mkdir "${remote_input_dir}" \
    --tar-in "${payload_tar}:${remote_input_dir}" \
    --run "${guest_script}"

  rm -rf -- "${staging_dir}"
  trap - EXIT
  ROOTLESS_STAGING_DIR=""
  log "done"
}

if [[ "${EUID}" -ne 0 ]]; then
  inject_with_libguestfs
  exit 0
fi

modprobe nbd max_part=16 || true

# Find a free /dev/nbdX
NBD_DEV=""
for dev in /dev/nbd{0..15}; do
  [[ -e "$dev" ]] || continue
  lsblk -no NAME "$dev" >/dev/null 2>&1 || continue
  qemu-nbd --disconnect "$dev" >/dev/null 2>&1 || true
  if [[ $(lsblk -lno NAME "$dev" 2>/dev/null | wc -l || true) -le 1 ]]; then
    NBD_DEV="$dev"; break
  fi
done
[[ -n "$NBD_DEV" ]] || fail "No free /dev/nbdX device found"

cleanup() {
  set +e
  if mountpoint -q "$MOUNT_DIR"; then dbg "umount $MOUNT_DIR"; umount "$MOUNT_DIR" || true; fi
  if [[ -n "$NBD_DEV" ]]; then dbg "qemu-nbd --disconnect $NBD_DEV"; qemu-nbd --disconnect "$NBD_DEV" || true; fi
}
trap cleanup EXIT

log "Connecting $QCOW -> $NBD_DEV"
qemu-nbd --connect "$NBD_DEV" "$QCOW"

# Pick a partition with a Linux fs, prefer the largest
udevadm settle || true
PART=""
for i in {1..6}; do
  partprobe "$NBD_DEV" >/dev/null 2>&1 || true
  partx -u "$NBD_DEV"   >/dev/null 2>&1 || true
  sleep 1
  dbg "pass $i partitions:"
  lsblk -lnpo NAME,TYPE,FSTYPE,SIZE "$NBD_DEV" || true
  PART="$(lsblk -lnpo NAME,FSTYPE,SIZE "$NBD_DEV" | awk '/(ext4|xfs|btrfs)/{print $1, $3}' | sort -k2 -h | tail -1 | awk '{print $1}')"
  [[ -n "$PART" ]] && break
done
if [[ -z "$PART" ]]; then
  FS="$(blkid -s TYPE -o value "$NBD_DEV" 2>/dev/null || true)"
  [[ "$FS" =~ ^(ext4|xfs|btrfs)$ ]] || fail "No valid filesystem on $NBD_DEV"
  PART="$NBD_DEV"; log "No partitions - using whole device as $FS"
fi

log "Mounting $PART at $MOUNT_DIR"
mkdir -p "$MOUNT_DIR"
mount "$PART" "$MOUNT_DIR"

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
  install -d "$base/flightctl/certs"
  install -m 0644 "$AGENT_DIR/config.yaml" "$base/flightctl/config.yaml"
  if compgen -G "$AGENT_DIR/certs/*" >/dev/null; then
    cp -a "$AGENT_DIR"/certs/. "$base/flightctl/certs/"
  fi
  if compgen -G "$base/flightctl/certs/*.key" >/dev/null; then
    chmod 600 "$base/flightctl/certs/"*.key || true
  fi
  chown -R root:root "$base/flightctl"
  sh -c "echo 'injected $(date -u +%FT%TZ)' > '$base/flightctl/INJECTION_OK'"
  dbg "tree:"
  ls -l "$base/flightctl" || true
  ls -l "$base/flightctl/certs" || true
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
  install -d "$target_dir"
  install -m 0644 "$E2E_CA" "$target_dir/ca.crt"
  chown -R root:root "$base/containers"

  local anchors_dir="$base/pki/ca-trust/source/anchors"
  log "Installing E2E CA to $anchors_dir/"
  install -d "$anchors_dir"
  install -m 0644 "$E2E_CA" "$anchors_dir/flightctl-e2e-registry.crt"

  local systemd_dir="$base/systemd/system"
  # Use mkdir -p rather than install -d to avoid setting the overlayfs opaque xattr,
  # which would hide symlinks (e.g. the bootc timer mask) already present in lower layers.
  mkdir -p "$systemd_dir" "$systemd_dir/multi-user.target.wants"
  chown root:root "$systemd_dir" "$systemd_dir/multi-user.target.wants"
  chmod 755 "$systemd_dir" "$systemd_dir/multi-user.target.wants"
  tee "$systemd_dir/flightctl-update-ca-trust.service" >/dev/null <<'UNIT'
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
  ln -sf "/etc/systemd/system/flightctl-update-ca-trust.service" \
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
  local tests_dest="${REG_TLS_HOSTPORT}/${TESTS_SOURCE_REPO_PATH}"
  log "Configuring registry remap $remap_file ($SOURCE_REPO -> $dest)"
  log "Configuring registry remap $remap_file (${SOURCE_REPO}-private -> $private_dest)"
  log "Configuring registry remap $remap_file ($TESTS_SOURCE_REPO -> $tests_dest)"
  install -d "$config_dir"
  tee "$remap_file" >/dev/null <<EOF
[[registry]]
prefix = "${SOURCE_REPO}"
location = "${dest}"

[[registry]]
prefix = "${SOURCE_REPO}-private"
location = "${private_dest}"

[[registry]]
prefix = "${TESTS_SOURCE_REPO}"
location = "${tests_dest}"
EOF
  chown root:root "$remap_file"
}

write_mirror_registry() {
  local base="$1"
  if [[ -z "${MIRROR_REGISTRY:-}" ]]; then
    log "No mirror registry configured - skipping"
    return
  fi
  local config_dir="$base/containers/registries.conf.d"
  local mirror_file="$config_dir/100-mirror-registry.conf"
  log "Configuring mirror registry remap: quay.io -> $MIRROR_REGISTRY"
  install -d "$config_dir"
  tee "$mirror_file" >/dev/null <<EOF
[[registry]]
prefix = "quay.io:443"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "quay.io"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "registry.redhat.io"
location = "${MIRROR_REGISTRY}"
insecure = true

[[registry]]
prefix = "registry.access.redhat.com"
location = "${MIRROR_REGISTRY}"
insecure = true
EOF
  chown root:root "$mirror_file"

  local policy_file="$base/containers/policy.json"
  log "Writing permissive policy.json for mirror registry"
  tee "$policy_file" >/dev/null <<EOF
{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {
    "docker": {
      "${MIRROR_REGISTRY}": [{"type": "insecureAcceptAnything"}]
    },
    "docker-daemon": {
      "": [{"type": "insecureAcceptAnything"}]
    }
  }
}
EOF
  chown root:root "$policy_file"
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
      if ! grep -qF -- "$host_fqdn" "$hosts_file" 2>/dev/null; then
        echo "$host_ip $host_fqdn $host_short" | tee -a "$hosts_file" >/dev/null
      else
        log "Hosts entry for $host_fqdn already exists"
      fi
    else
      tee "$hosts_file" >/dev/null <<EOF
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
$host_ip $host_fqdn $host_short
EOF
    fi
  else
    log "Host FQDN is localhost or empty - skipping host FQDN entry"
    # Ensure minimal hosts file exists for registry entry below
    if [[ ! -f "$hosts_file" ]]; then
      tee "$hosts_file" >/dev/null <<EOF
127.0.0.1   localhost localhost.localdomain
::1         localhost localhost.localdomain
EOF
    fi
  fi

  # For quadlet environment, add flightctl-vm.local entry
  if [[ -n "${QUADLET_HOST:-}" ]] && [[ "$QUADLET_HOST" != "localhost" ]]; then
    if ! grep -qF -- "flightctl-vm.local" "$hosts_file" 2>/dev/null; then
      log "Adding FlightCtl VM hosts entry: $QUADLET_HOST -> flightctl-vm.local"
      echo "$QUADLET_HOST flightctl-vm.local" | tee -a "$hosts_file" >/dev/null
    else
      log "FlightCtl VM hosts entry already exists"
    fi
  fi

  # Add registry hostname entry for IPv6 mode
  if [[ "${IPV6_ONLY:-false}" == "true" ]]; then
    if ! grep -qF -- "$REGISTRY_HOSTNAME" "$hosts_file" 2>/dev/null; then
      log "IPv6 mode: Adding hosts entry $host_ip -> $REGISTRY_HOSTNAME"
      echo "$host_ip $REGISTRY_HOSTNAME" | tee -a "$hosts_file" >/dev/null
    else
      log "IPv6 mode: Registry hostname entry already exists"
    fi
  fi

  chown root:root "$hosts_file"
  log "Hosts entry added successfully"
}

# Write to deployment etc so it appears at guest /etc
copy_into "$DEPLOY_ETC"
inject_registry_ca "$DEPLOY_ETC"
write_registry_remap "$DEPLOY_ETC"
write_mirror_registry "$DEPLOY_ETC"
inject_hosts_entry "$DEPLOY_ETC"

# Also mirror to on-disk etc so it shows under guest /sysroot/etc
if [[ "$SYSROOT_ETC" != "$DEPLOY_ETC" ]]; then
  copy_into "$SYSROOT_ETC"
  inject_registry_ca "$SYSROOT_ETC"
  write_registry_remap "$SYSROOT_ETC"
  write_mirror_registry "$SYSROOT_ETC"
  inject_hosts_entry "$SYSROOT_ETC"
fi

sync
log "done"
# cleanup by trap

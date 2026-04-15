#!/usr/bin/env bash
#
# Reproduces the helm 4 kstatus watcher "send on closed channel" panic
# that causes flaky e2e test failures in label 87531.
#
# This script:
#   1. Creates a VM from the e2e qcow2 image
#   2. Waits for enrollment and approves it
#   3. Updates the device to v12 (helm 4 + MicroShift)
#   4. Waits for reboot into v12
#   5. Copies the flake reproducer chart + script to the VM
#   6. Runs the reproducer (10 workers x 100 iterations = 1000 helm invocations)
#   7. Reports results
#
# Prerequisites (handled by the workflow):
#   - flightctl deployed to kind (make deploy / deploy-backend-with-helm)
#   - e2e environment prepared (make prepare-e2e-test)
#   - bin/flightctl CLI available and logged in
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

FLIGHTCTL="${PROJECT_ROOT}/bin/flightctl"
QCOW="${PROJECT_ROOT}/bin/output/qcow2/disk.qcow2"
SSH_KEY="${PROJECT_ROOT}/bin/.ssh/id_rsa"
VM_NAME="flake-repro-vm"
VM_DISK="/tmp/${VM_NAME}.qcow2"
SSH_PORT=2299
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR"
SSH_USER="user"
SSH_PASS="user"
FLAKE_WORKERS="${FLAKE_WORKERS:-10}"
FLAKE_RUNS="${FLAKE_RUNS:-100}"

# v12 image reference (same as used by e2e test 87531)
V12_IMAGE="quay.io/flightctl/flightctl-device:v12"

log() { echo "[$(date +%H:%M:%S)] $*"; }
fail() { log "FATAL: $*"; exit 1; }

ssh_vm() {
    if [ -f "$SSH_KEY" ]; then
        ssh -i "$SSH_KEY" -p "$SSH_PORT" $SSH_OPTS "${SSH_USER}@localhost" "$@"
    else
        sshpass -p "$SSH_PASS" ssh -p "$SSH_PORT" $SSH_OPTS "${SSH_USER}@localhost" "$@"
    fi
}

scp_to_vm() {
    if [ -f "$SSH_KEY" ]; then
        scp -i "$SSH_KEY" -P "$SSH_PORT" $SSH_OPTS "$@"
    else
        sshpass -p "$SSH_PASS" scp -P "$SSH_PORT" $SSH_OPTS "$@"
    fi
}

cleanup() {
    log "Cleaning up VM ${VM_NAME}..."
    virsh destroy "$VM_NAME" 2>/dev/null || true
    virsh undefine "$VM_NAME" --nvram 2>/dev/null || true
    virsh undefine "$VM_NAME" 2>/dev/null || true
    rm -f "$VM_DISK"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Step 0: Verify prerequisites
# ---------------------------------------------------------------------------
log "=== Step 0: Verify prerequisites ==="
[ -x "$FLIGHTCTL" ] || fail "flightctl CLI not found at $FLIGHTCTL"
[ -f "$QCOW" ] || fail "qcow2 image not found at $QCOW"
$FLIGHTCTL get devices -o name &>/dev/null || fail "flightctl CLI not logged in or API not reachable"
command -v virsh &>/dev/null || fail "virsh not found"
command -v sshpass &>/dev/null || log "WARNING: sshpass not found, will try SSH key auth"
log "Prerequisites OK"

# ---------------------------------------------------------------------------
# Step 1: Create VM from qcow2
# ---------------------------------------------------------------------------
log "=== Step 1: Create VM ==="

# Clean up any leftover VM with the same name
virsh destroy "$VM_NAME" 2>/dev/null || true
virsh undefine "$VM_NAME" --nvram 2>/dev/null || true
virsh undefine "$VM_NAME" 2>/dev/null || true
rm -f "$VM_DISK"

# Copy qcow2 (don't modify the original)
cp "$QCOW" "$VM_DISK"

# Resize disk to 15G to have room for MicroShift + images
qemu-img resize "$VM_DISK" 15G

# Create domain XML with SLIRP networking (port-forward SSH)
DOMAIN_XML=$(mktemp)
cat > "$DOMAIN_XML" <<XMLEOF
<domain type="kvm" xmlns:qemu="http://libvirt.org/schemas/domain/qemu/1.0">
  <name>${VM_NAME}</name>
  <memory unit="MiB">4096</memory>
  <memoryBacking>
    <source type="memfd"/>
    <access mode="shared"/>
  </memoryBacking>
  <vcpu>2</vcpu>
  <features>
    <acpi></acpi>
    <apic></apic>
  </features>
  <cpu mode='custom' check='none'>
    <model>Haswell-noTSX-IBRS</model>
  </cpu>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <os firmware='efi'>
    <type machine='q35'>hvm</type>
    <boot dev='hd'/>
    <firmware>
      <feature enabled='yes' name='secure-boot'/>
      <feature enabled='no' name='enrolled-keys'/>
    </firmware>
  </os>
  <devices>
    <serial type='pty'>
      <target type='isa-serial' port='0'>
        <model name='isa-serial'/>
      </target>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <disk device="disk" type="file">
      <driver name="qemu" type="qcow2"/>
      <source file="${VM_DISK}"/>
      <target bus="virtio" dev="vda"/>
    </disk>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
    <tpm model='tpm-tis'>
      <backend type='emulator' version='2.0'>
        <active_pcr_banks>
          <sha256/>
        </active_pcr_banks>
      </backend>
    </tpm>
  </devices>
  <qemu:commandline>
    <qemu:arg value='-netdev'/>
    <qemu:arg value='user,id=n0,hostfwd=tcp::${SSH_PORT}-:22'/>
    <qemu:arg value='-device'/>
    <qemu:arg value='virtio-net-pci,netdev=n0,bus=pcie.0,addr=0x10'/>
  </qemu:commandline>
</domain>
XMLEOF

virsh define "$DOMAIN_XML"
rm -f "$DOMAIN_XML"
virsh start "$VM_NAME"
log "VM started, SSH on localhost:${SSH_PORT}"

# ---------------------------------------------------------------------------
# Step 2: Wait for VM to boot and SSH to become available
# ---------------------------------------------------------------------------
log "=== Step 2: Wait for VM boot ==="
for i in $(seq 1 120); do
    if ssh_vm "true" 2>/dev/null; then
        log "SSH available after ${i}s"
        break
    fi
    if [ "$i" -eq 120 ]; then
        fail "VM did not become reachable via SSH within 120s"
    fi
    sleep 1
done

ssh_vm "cat /etc/os-release | head -3"
log "VM is up"

# ---------------------------------------------------------------------------
# Step 3: Wait for enrollment request and approve
# ---------------------------------------------------------------------------
log "=== Step 3: Wait for enrollment and approve ==="

# Get the enrollment ID from agent logs on the VM
ENROLLMENT_ID=""
for i in $(seq 1 60); do
    ENROLLMENT_ID=$(ssh_vm "sudo journalctl -u flightctl-agent --no-pager -b 2>/dev/null" 2>/dev/null | \
        grep -oP 'Bootstrapping device: \K\S+' | tail -1 || true)
    if [ -n "$ENROLLMENT_ID" ]; then
        log "Enrollment ID: $ENROLLMENT_ID"
        break
    fi
    if [ "$i" -eq 60 ]; then
        fail "Could not find enrollment ID in agent logs after 60s"
    fi
    sleep 1
done

# Wait for enrollment request to appear in flightctl
for i in $(seq 1 60); do
    if $FLIGHTCTL get enrollmentrequest "$ENROLLMENT_ID" &>/dev/null; then
        log "Enrollment request found"
        break
    fi
    if [ "$i" -eq 60 ]; then
        fail "Enrollment request $ENROLLMENT_ID not found after 60s"
    fi
    sleep 1
done

# Approve enrollment
$FLIGHTCTL approve enrollmentrequest "$ENROLLMENT_ID"
log "Enrollment approved"

# Wait for device to start reporting status
for i in $(seq 1 60); do
    if $FLIGHTCTL get device "$ENROLLMENT_ID" &>/dev/null; then
        log "Device is reporting"
        break
    fi
    if [ "$i" -eq 60 ]; then
        fail "Device $ENROLLMENT_ID not reporting after 60s"
    fi
    sleep 2
done

# Wait for device to be UpToDate on initial spec (v1)
log "Waiting for device to be UpToDate on initial spec..."
for i in $(seq 1 120); do
    STATUS=$($FLIGHTCTL get device "$ENROLLMENT_ID" 2>&1 | tail -1 || true)
    if echo "$STATUS" | grep -q "UpToDate"; then
        log "Device is UpToDate: $STATUS"
        break
    fi
    if [ "$i" -eq 120 ]; then
        fail "Device not UpToDate after 120s: $STATUS"
    fi
    sleep 2
done

# ---------------------------------------------------------------------------
# Step 4: Update device to v12 (MicroShift + helm 4)
# ---------------------------------------------------------------------------
log "=== Step 4: Update device to v12 ==="

cat <<EOF | $FLIGHTCTL apply -f -
apiVersion: flightctl.io/v1beta1
kind: Device
metadata:
  name: ${ENROLLMENT_ID}
spec:
  os:
    image: ${V12_IMAGE}
EOF
log "Applied v12 OS spec"

# Wait for the update to start (device goes to Updating)
log "Waiting for OS update to start..."
for i in $(seq 1 60); do
    STATUS=$($FLIGHTCTL get device "$ENROLLMENT_ID" 2>&1 | tail -1 || true)
    if echo "$STATUS" | grep -qE "Updating|Rebooting"; then
        log "Update in progress: $STATUS"
        break
    fi
    if [ "$i" -eq 60 ]; then
        log "WARNING: Did not see Updating status, continuing anyway: $STATUS"
        break
    fi
    sleep 2
done

# Wait for reboot (SSH will drop)
log "Waiting for VM to reboot into v12..."
REBOOT_DETECTED=false
for i in $(seq 1 300); do
    if ! ssh_vm "true" 2>/dev/null; then
        if [ "$REBOOT_DETECTED" = false ]; then
            log "SSH dropped - reboot in progress"
            REBOOT_DETECTED=true
        fi
    else
        if [ "$REBOOT_DETECTED" = true ]; then
            log "VM is back after reboot (${i}s)"
            break
        fi
    fi
    if [ "$i" -eq 300 ]; then
        fail "VM did not come back after reboot within 300s"
    fi
    sleep 1
done

# If reboot was not detected via SSH drop, just wait for UpToDate
if [ "$REBOOT_DETECTED" = false ]; then
    log "Reboot not detected via SSH drop, waiting for UpToDate..."
fi

# ---------------------------------------------------------------------------
# Step 5: Wait for v12 to be fully up (UpToDate + microshift + helm)
# ---------------------------------------------------------------------------
log "=== Step 5: Wait for v12 to be fully operational ==="

# Wait for device to report UpToDate after v12 switch
for i in $(seq 1 300); do
    STATUS=$($FLIGHTCTL get device "$ENROLLMENT_ID" 2>&1 | tail -1 || true)
    if echo "$STATUS" | grep -q "UpToDate"; then
        log "Device UpToDate on v12: $STATUS"
        break
    fi
    if echo "$STATUS" | grep -q "OutOfDate.*Error"; then
        fail "Update to v12 failed: $STATUS"
    fi
    if [ "$((i % 30))" -eq 0 ]; then
        log "Still waiting for v12 UpToDate (${i}s): $STATUS"
    fi
    sleep 2
done

# Verify helm and microshift are available
ssh_vm "helm version --short 2>&1" || fail "helm not available on VM"
ssh_vm "sudo systemctl is-active microshift 2>&1" || log "WARNING: microshift not active yet"

# Wait for microshift kubeconfig to appear
log "Waiting for MicroShift kubeconfig..."
for i in $(seq 1 120); do
    if ssh_vm "sudo test -f /var/lib/microshift/resources/kubeadmin/kubeconfig" 2>/dev/null; then
        log "MicroShift kubeconfig available"
        break
    fi
    if [ "$i" -eq 120 ]; then
        fail "MicroShift kubeconfig not found after 120s"
    fi
    sleep 2
done

# Wait for MicroShift API to be ready
log "Waiting for MicroShift API readiness..."
for i in $(seq 1 120); do
    if ssh_vm "sudo kubectl --kubeconfig /var/lib/microshift/resources/kubeadmin/kubeconfig get nodes" 2>/dev/null; then
        log "MicroShift API is ready"
        break
    fi
    if [ "$i" -eq 120 ]; then
        fail "MicroShift API not ready after 120s"
    fi
    sleep 2
done

# ---------------------------------------------------------------------------
# Step 6: Copy flake reproducer files to VM
# ---------------------------------------------------------------------------
log "=== Step 6: Copy flake reproducer files ==="

scp_to_vm -r "${SCRIPT_DIR}/failing-chart" "${SSH_USER}@localhost:/tmp/"
scp_to_vm "${SCRIPT_DIR}/flake.sh" "${SSH_USER}@localhost:/tmp/"
ssh_vm "chmod +x /tmp/flake.sh"

# Verify files arrived
ssh_vm "ls -la /tmp/flake.sh /tmp/failing-chart/Chart.yaml /tmp/failing-chart/values.yaml /tmp/failing-chart/templates/deployment.yaml"
log "Files copied successfully"

# Pre-pull the alpine image into CRI-O
log "Pre-pulling alpine image into CRI-O..."
ssh_vm "sudo crictl pull quay.io/flightctl-tests/alpine:v1 2>&1" || log "WARNING: crictl pull failed (may already be cached)"

# ---------------------------------------------------------------------------
# Step 7: Run the flake reproducer
# ---------------------------------------------------------------------------
log "=== Step 7: Run flake reproducer (${FLAKE_WORKERS} workers x ${FLAKE_RUNS} runs) ==="

ssh_vm "sudo FLAKE_WORKERS=${FLAKE_WORKERS} FLAKE_RUNS=${FLAKE_RUNS} /tmp/flake.sh" 2>&1 | tee /tmp/flake-output.txt
FLAKE_RC=${PIPESTATUS[0]}

# ---------------------------------------------------------------------------
# Step 8: Collect results
# ---------------------------------------------------------------------------
log "=== Step 8: Results ==="

# Copy logs from VM
mkdir -p /tmp/flake-results
scp_to_vm -r "${SSH_USER}@localhost:/tmp/flake-logs/" /tmp/flake-results/ 2>/dev/null || true

if [ "$FLAKE_RC" -ne 0 ]; then
    log "PANIC REPRODUCED! Exit code: $FLAKE_RC"
    log "Full logs in /tmp/flake-results/"
    # Show panic details from worker logs
    for logfile in /tmp/flake-results/flake-logs/worker-*.log; do
        if [ -f "$logfile" ] && grep -q "PANIC" "$logfile"; then
            log "=== $(basename "$logfile") ==="
            cat "$logfile"
        fi
    done
    exit 1
else
    log "No panics detected in ${FLAKE_WORKERS}x${FLAKE_RUNS} = $((FLAKE_WORKERS * FLAKE_RUNS)) helm invocations"
    exit 0
fi

#!/usr/bin/env bash
set -euo pipefail

# Get the project root directory
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "🔄 [Cleanup] Starting global E2E test cleanup..."

VIRSH="virsh --connect qemu:///session"

# Find all flightctl e2e VMs using virsh
echo "🔄 [Cleanup] Finding flightctl e2e VMs..."
if ! vm_output=$($VIRSH list --all --name 2>/dev/null); then
    echo "⚠️  [Cleanup] Failed to list VMs: virsh may not be available or accessible"
    exit 0
fi

# Filter for flightctl e2e VMs (includes both pool VMs and imagebuild test VMs)
flightctl_vms=()
while IFS= read -r vm_name; do
    if [[ -n "$vm_name" && ( "$vm_name" == flightctl-e2e-* || "$vm_name" == imagebuild-test-* ) ]]; then
        flightctl_vms+=("$vm_name")
    fi
done <<< "$vm_output"

echo "🔍 [Cleanup] Found ${#flightctl_vms[@]} flightctl e2e VMs: ${flightctl_vms[*]}"

# Clean up each VM
for vm_name in "${flightctl_vms[@]}"; do
    echo "🔄 [Cleanup] Cleaning up VM: $vm_name"

    # 1. Delete pristine snapshot (ignore errors)
    echo "🔄 [Cleanup] Deleting pristine snapshot for $vm_name"
    if ! $VIRSH snapshot-delete "$vm_name" "pristine" --metadata 2>/dev/null; then
        echo "⚠️  [Cleanup] Failed to delete pristine snapshot for $vm_name (may not exist)"
    fi

    # 2. Destroy the VM if it's running (ignore errors)
    echo "🔄 [Cleanup] Destroying VM: $vm_name"
    if ! $VIRSH destroy "$vm_name" 2>/dev/null; then
        echo "⚠️  [Cleanup] Failed to destroy $vm_name (may not be running)"
    fi

    # 3. Undefine the domain (try multiple approaches)
    echo "🔄 [Cleanup] Undefining domain: $vm_name"
    if $VIRSH undefine "$vm_name" 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name"
    elif $VIRSH undefine "$vm_name" --nvram 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with NVRAM)"
    elif $VIRSH undefine "$vm_name" --remove-all-storage --nvram 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with storage and NVRAM)"
    else
        echo "❌ [Cleanup] Failed to undefine $vm_name with all approaches"
    fi
done

# Clean up temporary directories in /tmp
echo "🔄 [Cleanup] Cleaning up temporary directories..."
if tmp_dirs=$(find /tmp -maxdepth 1 -name "flightctl-e2e-worker-*" -type d 2>/dev/null); then
    if [[ -n "$tmp_dirs" ]]; then
        echo "🔍 [Cleanup] Found temporary directories:"
        echo "$tmp_dirs"
        echo "🔄 [Cleanup] Removing temporary directories..."
        # Use find with -delete for safer removal
        if find /tmp -maxdepth 1 -name "flightctl-e2e-worker-*" -type d -exec rm -rf {} + 2>/dev/null; then
            echo "✅ [Cleanup] Successfully removed temporary directories"
        else
            echo "⚠️  [Cleanup] Failed to remove some temporary directories"
        fi
    else
        echo "✅ [Cleanup] No temporary directories found"
    fi
else
    echo "⚠️  [Cleanup] Failed to search for temporary directories"
fi

echo "✅ [Cleanup] Global test cleanup completed"
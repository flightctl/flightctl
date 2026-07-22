#!/usr/bin/env bash
set -euo pipefail

# Get the project root directory
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "🔄 [Cleanup] Starting global E2E test cleanup..."

# Find all flightctl e2e VMs using virsh
echo "🔄 [Cleanup] Finding flightctl e2e VMs..."
if ! vm_output=$(virsh list --all --name 2>/dev/null); then
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
    if ! virsh snapshot-delete "$vm_name" "pristine" --metadata 2>/dev/null; then
        echo "⚠️  [Cleanup] Failed to delete pristine snapshot for $vm_name (may not exist)"
    fi

    # 2. Destroy the VM if it's running (ignore errors)
    echo "🔄 [Cleanup] Destroying VM: $vm_name"
    if ! virsh destroy "$vm_name" 2>/dev/null; then
        echo "⚠️  [Cleanup] Failed to destroy $vm_name (may not be running)"
    fi

    # 3. Undefine the domain (try multiple approaches)
    echo "🔄 [Cleanup] Undefining domain: $vm_name"
    if virsh undefine "$vm_name" 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name"
    elif virsh undefine "$vm_name" --nvram 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with NVRAM)"
    elif virsh undefine "$vm_name" --remove-all-storage --nvram 2>/dev/null; then
        echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with storage and NVRAM)"
    else
        echo "❌ [Cleanup] Failed to undefine $vm_name with all approaches"
    fi
done

# Clean up session-mode VMs (e.g. TPM passthrough VMs created via qemu:///session)
echo "🔄 [Cleanup] Finding session-mode flightctl e2e VMs..."
if session_output=$(virsh -c qemu:///session list --all --name 2>/dev/null); then
    session_vms=()
    while IFS= read -r vm_name; do
        if [[ -n "$vm_name" && ( "$vm_name" == flightctl-e2e-* || "$vm_name" == imagebuild-test-* ) ]]; then
            session_vms+=("$vm_name")
        fi
    done <<< "$session_output"

    echo "🔍 [Cleanup] Found ${#session_vms[@]} session-mode flightctl e2e VMs: ${session_vms[*]}"

    for vm_name in "${session_vms[@]}"; do
        echo "🔄 [Cleanup] Cleaning up session VM: $vm_name"

        if ! virsh -c qemu:///session snapshot-delete "$vm_name" "pristine" --metadata 2>/dev/null; then
            echo "⚠️  [Cleanup] Failed to delete pristine snapshot for session VM $vm_name (may not exist)"
        fi

        virsh -c qemu:///session destroy "$vm_name" 2>/dev/null || true

        if virsh -c qemu:///session undefine "$vm_name" --snapshots-metadata 2>/dev/null; then
            echo "✅ [Cleanup] Successfully cleaned up session VM: $vm_name"
        elif virsh -c qemu:///session undefine "$vm_name" --snapshots-metadata --nvram 2>/dev/null; then
            echo "✅ [Cleanup] Successfully cleaned up session VM: $vm_name (with NVRAM)"
        elif virsh -c qemu:///session undefine "$vm_name" --snapshots-metadata --remove-all-storage --nvram 2>/dev/null; then
            echo "✅ [Cleanup] Successfully cleaned up session VM: $vm_name (with storage and NVRAM)"
        else
            echo "❌ [Cleanup] Failed to undefine session VM $vm_name with all approaches"
        fi
    done
else
    echo "⚠️  [Cleanup] Failed to list session-mode VMs (qemu:///session may not be available)"
fi

# Clean up temporary directories in /tmp
echo "🔄 [Cleanup] Cleaning up temporary directories..."
if tmp_dirs=$(find /tmp -maxdepth 1 -name "flightctl-e2e-*" -type d 2>/dev/null); then
    if [[ -n "$tmp_dirs" ]]; then
        echo "🔍 [Cleanup] Found temporary directories:"
        echo "$tmp_dirs"
        echo "🔄 [Cleanup] Removing temporary directories..."
        if find /tmp -maxdepth 1 -name "flightctl-e2e-*" -type d -exec rm -rf {} + 2>/dev/null; then
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

# Clean up container-backed devices (see ContainerDevice) - these are plain podman/docker
# containers, not libvirt domains, so virsh cleanup above never touches them.
echo "🔄 [Cleanup] Finding flightctl e2e container-backed devices..."
container_cli=""
if command -v podman &>/dev/null; then
    container_cli="podman"
elif command -v docker &>/dev/null; then
    container_cli="docker"
fi

if [[ -n "$container_cli" ]]; then
    if container_names=$("$container_cli" ps -a --filter "name=flightctl-e2e-container-" --format "{{.Names}}" 2>/dev/null); then
        if [[ -n "$container_names" ]]; then
            echo "🔍 [Cleanup] Found flightctl e2e container-backed devices:"
            echo "$container_names"
            while IFS= read -r container_name; do
                [[ -n "$container_name" ]] || continue
                if "$container_cli" rm -f -v "$container_name" &>/dev/null; then
                    echo "✅ [Cleanup] Successfully removed container device: $container_name"
                else
                    echo "⚠️  [Cleanup] Failed to remove container device: $container_name"
                fi
            done <<< "$container_names"
        else
            echo "✅ [Cleanup] No container-backed devices found"
        fi
    else
        echo "⚠️  [Cleanup] Failed to list containers via $container_cli"
    fi
else
    echo "⚠️  [Cleanup] Neither podman nor docker available - skipping container-backed device cleanup"
fi

echo "✅ [Cleanup] Global test cleanup completed"
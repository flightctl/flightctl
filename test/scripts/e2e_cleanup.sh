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

# Filter for flightctl e2e VMs
flightctl_vms=()
while IFS= read -r vm_name; do
    if [[ -n "$vm_name" && "$vm_name" == flightctl-e2e-* ]]; then
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

# Step 3b: Global cleanup of EnrollmentRequests...
echo "🔄 [Test Execution] Step 3b: Cleaning up leftover EnrollmentRequests..."

if [ -x "${FLIGHTCTL_BIN}" ]; then
    # Returns lines like: enrollmentrequest/<name>
    ERS="$(${FLIGHTCTL_BIN} get er -o name 2>/dev/null || true)"

    if [ -n "${ERS}" ]; then
        echo "Found EnrollmentRequests to evaluate:"
        echo "${ERS}"

        # Process each ER name one-by-one
        echo "${ERS}" | sed 's!.*/!!' | while IFS= read -r ername; do
            # Skip empty lines
            [ -z "${ername}" ] && continue

            # Check if a Device with the same name exists. If it does, skip ER deletion.
            if ${FLIGHTCTL_BIN} get device "${ername}" -o name >/dev/null 2>&1; then
                echo "Skipping deletion of EnrollmentRequest '${ername}': device exists"
                continue
            fi

            # Attempt deletion of the ER. Treat "device exists" as non-fatal (race).
            if output="$(${FLIGHTCTL_BIN} delete er "${ername}" 2>&1)"; then
                echo "Deleted EnrollmentRequest '${ername}'"
            else
                # If a race occurred and the server reports device exists, skip; otherwise surface the error.
                if echo "${output}" | grep -qi "device exists"; then
                    echo "Skipping EnrollmentRequest '${ername}': server reports device exists (race)"
                    continue
                fi
                echo "Error deleting EnrollmentRequest '${ername}': ${output}"
                exit 1
            fi
        done
    else
        echo "No EnrollmentRequests found, nothing to delete."
    fi
else
    echo "⚠️  flightctl binary not found at ${FLIGHTCTL_BIN}, skipping EnrollmentRequest cleanup."
fi

# Exit with the test exit code
echo "✅ [Cleanup] Global test cleanup completed"
exit $TEST_EXIT_CODE


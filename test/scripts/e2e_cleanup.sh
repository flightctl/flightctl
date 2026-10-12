#!/usr/bin/env bash
set -euo pipefail

echo "🔄 [Cleanup] Starting E2E VM cleanup for uid $(id -u)..."

cleanup_virsh_connection() {
    local uri="$1"
    local label="$2"
    local vm_output

    if ! vm_output="$(virsh -c "${uri}" list --all --name 2>/dev/null)"; then
        echo "⚠️  [Cleanup] Skipping ${label}; libvirt connection ${uri} is unavailable."
        return 0
    fi

    local -a flightctl_vms=()
    while IFS= read -r vm_name; do
        if [[ -n "${vm_name}" && ( "${vm_name}" == flightctl-e2e-* || "${vm_name}" == imagebuild-test-* ) ]]; then
            flightctl_vms+=("${vm_name}")
        fi
    done <<< "${vm_output}"

    echo "🔍 [Cleanup] Found ${#flightctl_vms[@]} ${label} VMs: ${flightctl_vms[*]}"
    for vm_name in "${flightctl_vms[@]}"; do
        echo "🔄 [Cleanup] Cleaning up ${label} VM: ${vm_name}"
        virsh -c "${uri}" snapshot-delete "${vm_name}" pristine --metadata 2>/dev/null || true
        virsh -c "${uri}" destroy "${vm_name}" 2>/dev/null || true

        if virsh -c "${uri}" undefine "${vm_name}" --snapshots-metadata 2>/dev/null; then
            continue
        elif virsh -c "${uri}" undefine "${vm_name}" --snapshots-metadata --nvram 2>/dev/null; then
            continue
        elif virsh -c "${uri}" undefine "${vm_name}" --snapshots-metadata --remove-all-storage --nvram 2>/dev/null; then
            continue
        fi
        echo "⚠️  [Cleanup] Could not undefine ${vm_name} in ${label}."
    done
}

if command -v virsh >/dev/null 2>&1; then
    if [[ "$(id -u)" -eq 0 ]]; then
        cleanup_virsh_connection qemu:///system system
    fi
    cleanup_virsh_connection qemu:///session session
else
    echo "⚠️  [Cleanup] virsh is unavailable; skipping VM cleanup."
fi

echo "🔄 [Cleanup] Cleaning up this user's temporary E2E directories..."
if command -v find >/dev/null 2>&1; then
    uid="$(id -u)"
    if tmp_dirs="$(find /tmp -maxdepth 1 -uid "${uid}" -name 'flightctl-e2e-*' -type d -print 2>/dev/null)"; then
        while IFS= read -r tmp_dir; do
            [[ -n "${tmp_dir}" ]] || continue
            if ! other_owner="$(find "${tmp_dir}" -xdev ! -uid "${uid}" -print -quit 2>/dev/null)"; then
                echo "⚠️  [Cleanup] Leaving ${tmp_dir}; ownership could not be checked."
            elif [[ -n "${other_owner}" ]]; then
                echo "⚠️  [Cleanup] Leaving ${tmp_dir}; it contains files not owned by uid ${uid}."
            elif ! find "${tmp_dir}" -xdev -depth -delete; then
                echo "⚠️  [Cleanup] Failed to remove ${tmp_dir} without crossing filesystem boundaries."
            fi
        done <<< "${tmp_dirs}"
    else
        echo "⚠️  [Cleanup] Could not inspect /tmp; leaving temporary E2E directories in place."
    fi
fi

echo "✅ [Cleanup] E2E VM cleanup completed."

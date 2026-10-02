#!/usr/bin/env bash
set -euo pipefail

# Get the project root directory
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# is_e2e_vm_name reports whether virsh listed a domain this script should destroy.
is_e2e_vm_name() {
	local vm_name="$1"
	[[ -n "$vm_name" && ( "$vm_name" == flightctl-e2e-* || "$vm_name" == imagebuild-test-* ) ]]
}

# virsh_uri URI ARGS...  Empty URI uses the default connection.
virsh_uri() {
	local uri="$1"
	shift
	if [[ -n "$uri" ]]; then
		virsh -c "$uri" "$@"
	else
		virsh "$@"
	fi
}

# External snapshots (pool "pristine") leave overlay + memory files. Prefer a
# real snapshot-delete; --metadata is only a fallback so undefine can proceed.
delete_all_snapshots() {
	local uri="$1"
	local vm_name="$2"
	local snap

	# Newest/children first when libvirt prints a tree; ignore empty lines.
	mapfile -t snaps < <(virsh_uri "$uri" snapshot-list "$vm_name" --name 2>/dev/null | awk 'NF {print}' || true)
	if [[ ${#snaps[@]} -eq 0 ]]; then
		echo "ℹ️  [Cleanup] No snapshots listed for $vm_name"
		return 0
	fi

	echo "🔍 [Cleanup] Snapshots on $vm_name: ${snaps[*]}"
	for snap in "${snaps[@]}"; do
		echo "🔄 [Cleanup] Deleting snapshot $snap on $vm_name"
		if virsh_uri "$uri" snapshot-delete "$vm_name" "$snap" --children 2>/dev/null; then
			echo "✅ [Cleanup] Deleted snapshot $snap (including children)"
			continue
		fi
		if virsh_uri "$uri" snapshot-delete "$vm_name" "$snap" 2>/dev/null; then
			echo "✅ [Cleanup] Deleted snapshot $snap"
			continue
		fi
		if virsh_uri "$uri" snapshot-delete "$vm_name" "$snap" --metadata --children 2>/dev/null; then
			echo "⚠️  [Cleanup] Removed snapshot metadata for $snap (disk/memory files may remain)"
			continue
		fi
		echo "⚠️  [Cleanup] Failed to delete snapshot $snap for $vm_name"
	done
}

# undefine_domain removes the domain definition after snapshots are deleted.
undefine_domain() {
	local uri="$1"
	local vm_name="$2"

	echo "🔄 [Cleanup] Undefining domain: $vm_name"
	if virsh_uri "$uri" undefine "$vm_name" --snapshots-metadata 2>/dev/null; then
		echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name"
	elif virsh_uri "$uri" undefine "$vm_name" --snapshots-metadata --nvram 2>/dev/null; then
		echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with NVRAM)"
	elif virsh_uri "$uri" undefine "$vm_name" --snapshots-metadata --remove-all-storage --nvram 2>/dev/null; then
		echo "✅ [Cleanup] Successfully cleaned up VM: $vm_name (with storage and NVRAM)"
	else
		echo "❌ [Cleanup] Failed to undefine $vm_name with all approaches"
	fi
}

# list_e2e_domain_names URI [virsh list flags...]
# Prints e2e domain names. Returns 1 if virsh list fails.
list_e2e_domain_names() {
	local uri="$1"
	shift
	local vm_output vm_name
	if ! vm_output=$(virsh_uri "$uri" list "$@"); then
		return 1
	fi
	while IFS= read -r vm_name; do
		if is_e2e_vm_name "$vm_name"; then
			printf '%s\n' "$vm_name"
		fi
	done <<<"$vm_output"
	return 0
}

# destroy_if_active force-stops a domain that is still active.
# libvirt's "in shutdown" state means shutdown is in progress; the guest is
# still active, and undefine does not stop it. Only "shut off" skips destroy.
# State lookup and destroy errors propagate.
destroy_if_active() {
	local uri="$1"
	local vm_name="$2"
	local state
	if ! state=$(virsh_uri "$uri" domstate "$vm_name"); then
		echo "❌ [Cleanup] Failed to get domain state for $vm_name"
		return 1
	fi
	case "$(echo "$state" | tr '[:upper:]' '[:lower:]')" in
	shut\ off)
		echo "ℹ️  [Cleanup] VM $vm_name is shut off, skipping destroy"
		return 0
		;;
	esac
	echo "🔄 [Cleanup] Destroying VM: $vm_name"
	if ! virsh_uri "$uri" destroy "$vm_name"; then
		echo "❌ [Cleanup] Failed to destroy $vm_name"
		return 1
	fi
	return 0
}

# assert_no_active_e2e_guests fails if listing fails or any e2e domain is active.
# virsh list --name (without --state-running) includes running, paused,
# shutdown, and crashed domains.
assert_no_active_e2e_guests() {
	local uri="$1"
	local label="$2"
	local active
	if ! active=$(list_e2e_domain_names "$uri" --name); then
		echo "❌ [Cleanup] Failed to list active VMs on ${label}"
		return 1
	fi
	if [[ -n "$active" ]]; then
		echo "❌ [Cleanup] e2e VMs still active on ${label}: ${active//$'\n'/ }"
		return 1
	fi
	return 0
}

# cleanup_connection lists e2e domains on uri, deletes snapshots, destroys, and
# undefines them. label is the only string printed for the connection (not the URI).
# Returns non-zero if listing fails, destroy of an active domain fails, or a guest
# is still active afterward.
cleanup_connection() {
	local uri="$1"
	local label="$2"

	echo "🔄 [Cleanup] Finding flightctl e2e VMs on ${label}..."
	local vm_output
	if ! vm_output=$(list_e2e_domain_names "$uri" --all --name); then
		echo "❌ [Cleanup] Failed to list VMs on ${label}"
		return 1
	fi

	local flightctl_vms=()
	local vm_name
	while IFS= read -r vm_name; do
		[[ -n "$vm_name" ]] || continue
		flightctl_vms+=("$vm_name")
	done <<<"$vm_output"

	echo "🔍 [Cleanup] Found ${#flightctl_vms[@]} flightctl e2e VMs on ${label}: ${flightctl_vms[*]}"

	for vm_name in "${flightctl_vms[@]}"; do
		echo "🔄 [Cleanup] Cleaning up VM: $vm_name (${label})"
		delete_all_snapshots "$uri" "$vm_name"

		if ! destroy_if_active "$uri" "$vm_name"; then
			return 1
		fi

		undefine_domain "$uri" "$vm_name"
	done

	assert_no_active_e2e_guests "$uri" "$label"
}

# default_uri_is_session is true only when `virsh uri` confirms the default
# connection is qemu:///session. A failed lookup is not confirmation.
default_uri_is_session() {
	local uri
	uri=$(virsh_uri "" uri) || return 1
	[[ "${uri%%\?*}" == "qemu:///session" ]]
}

# cleanup_tracked_connections removes e2e guests on the primary connection and
# on qemu:///session. Hypervisor guests use E2E_LIBVIRT_URI (qemu+ssh://…/system).
# Nested e2e disks under /tmp belong to qemu:///session. SESSION_CLEAN is set
# only after that connection is confirmed clean. The default connection is not
# treated as session unless `virsh uri` says so.
cleanup_tracked_connections() {
	PRIMARY_URI="${E2E_LIBVIRT_URI:-}"
	PRIMARY_NOQUERY="${PRIMARY_URI%%\?*}"
	PRIMARY_CLEAN=0
	SESSION_CLEAN=0

	if [[ -n "$PRIMARY_URI" ]]; then
		if cleanup_connection "$PRIMARY_URI" "configured libvirt URI"; then
			PRIMARY_CLEAN=1
		fi
	else
		if cleanup_connection "" "default libvirt URI"; then
			PRIMARY_CLEAN=1
		fi
		if default_uri_is_session; then
			PRIMARY_NOQUERY="qemu:///session"
		fi
	fi

	if [[ "$PRIMARY_NOQUERY" == "qemu:///session" ]]; then
		SESSION_CLEAN=$PRIMARY_CLEAN
	elif cleanup_connection "qemu:///session" "qemu:///session"; then
		SESSION_CLEAN=1
	fi
}

# is_safe_e2e_disk_dir is true when dir is absolute, has no ".." components,
# and contains no glob metacharacters.
is_safe_e2e_disk_dir() {
	local dir="$1"
	local seg
	[[ -n "$dir" && "$dir" == /* ]] || return 1
	[[ "$dir" != *'*'* && "$dir" != *'?'* && "$dir" != *'['* ]] || return 1
	IFS=/ read -ra segs <<<"$dir"
	for seg in "${segs[@]}"; do
		[[ "$seg" != '..' ]] || return 1
	done
	return 0
}
# cleanup_disk_dir removes flightctl-e2e-worker-* and flightctl-e2e-fresh-*
# directories immediately under dir. Shared base disks are left in place.
cleanup_disk_dir() {
	local dir="$1"
	if ! is_safe_e2e_disk_dir "$dir"; then
		echo "⚠️  [Cleanup] Refusing to clean disk dir (must be an absolute path without '..' or glob characters): $dir"
		return 0
	fi
	[[ -d "$dir" ]] || return 0
	echo "🔍 [Cleanup] Removing e2e worker dirs under $dir"
	find "$dir" -maxdepth 1 \( -name "flightctl-e2e-worker-*" -o -name "flightctl-e2e-fresh-*" \) -exec rm -rf {} + 2>/dev/null || \
		echo "⚠️  [Cleanup] Failed to remove some directories under $dir"
}

# Overlay disks, external snapshot memory files, cloud-init ISOs.
main() {
	echo "🔄 [Cleanup] Starting global E2E test cleanup..."
	cleanup_tracked_connections

	echo "🔄 [Cleanup] Cleaning up temporary directories..."
	local cleanup_status=0
	if [[ "$SESSION_CLEAN" -eq 1 ]]; then
		cleanup_disk_dir /tmp
	else
		echo "⚠️  [Cleanup] Skipping /tmp disk cleanup; e2e guests may still be active or listing failed"
		cleanup_status=1
	fi
	if [[ -n "${E2E_VM_DISK_DIR:-}" ]]; then
		if [[ "$PRIMARY_CLEAN" -eq 1 ]]; then
			cleanup_disk_dir "${E2E_VM_DISK_DIR}"
		else
			echo "⚠️  [Cleanup] Skipping E2E_VM_DISK_DIR cleanup; e2e guests may still be active or listing failed"
			cleanup_status=1
		fi
	elif [[ "$PRIMARY_CLEAN" -ne 1 ]]; then
		cleanup_status=1
	fi

	if [[ "$cleanup_status" -ne 0 ]]; then
		echo "❌ [Cleanup] Global test cleanup did not finish cleanly"
		exit 1
	fi

	echo "✅ [Cleanup] Global test cleanup completed"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi

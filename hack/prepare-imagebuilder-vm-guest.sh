#!/usr/bin/env bash
set -euo pipefail

# The prepared disk is a build input, never downloaded or provisioned by a worker.
output_dir=${1:-.output/flightctl-vm}
mkdir -p "$output_dir"
output_dir=$(realpath "$output_dir")
if [[ ${VM_GUEST_SOURCE_URL+x} != ${VM_GUEST_SOURCE_SHA256+x} ]]; then
    echo 'Set VM_GUEST_SOURCE_URL and VM_GUEST_SOURCE_SHA256 together.' >&2
    exit 1
fi
source_url=${VM_GUEST_SOURCE_URL:-https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2}
source_sha256=${VM_GUEST_SOURCE_SHA256:-846574c8a97cd2d8dc1f231062d73107cc85cbbbda56335e264a46e3a6c8ab2f}
source_image=${VM_GUEST_SOURCE_IMAGE:-$output_dir/source.disk}

for program in qemu-system-x86_64 qemu-img python3 timeout curl; do
    command -v "$program" >/dev/null || { echo "Required program missing: $program" >&2; exit 1; }
done
if command -v genisoimage >/dev/null; then
    iso_command=(genisoimage)
else
    command -v xorriso >/dev/null || { echo 'Install genisoimage or xorriso' >&2; exit 1; }
    iso_command=(xorriso -as mkisofs)
fi

if [[ ! -f "$source_image" ]]; then
    curl --fail --location --connect-timeout 20 --max-time 600 --retry 3 --output "$source_image.download" "$source_url"
    mv "$source_image.download" "$source_image"
fi
printf '%s  %s\n' "$source_sha256" "$source_image" | sha256sum --check --strict
work_dir=$(mktemp -d "$output_dir/prepare-XXXXXX")
trap 'rm -rf "$work_dir"' EXIT
mkdir "$work_dir/share"

python3 - "$work_dir" "$source_url" "$source_sha256" <<'PY'
import json
from pathlib import Path
import sys

work = Path(sys.argv[1])
provision = '''#!/usr/bin/env bash
set -euxo pipefail
trap 'rc=$?; if ((rc)); then echo GUEST_PREPARATION_FAILED > /dev/ttyS0; fi; sync; poweroff' EXIT
# Updating the kernel and its matching modules before sealing avoids dependence
# on package downloads, installation or a reboot during a build/export attempt.
dnf -y upgrade
dnf -y install podman buildah qemu-img e2fsprogs util-linux kernel-modules-extra cloud-init openssh-server
systemctl enable sshd
mkdir -p /mnt/manifest
mount -t 9p -o trans=virtio,version=9p2000.L hostshare /mnt/manifest
rpm -qa --qf '%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\\n' | sort > /mnt/manifest/guest.rpms
# Verify the next boot has the modules used by the launcher, even when the
# provisioning boot is still running the original cloud image kernel.
find /usr/lib/modules -name '9p.ko*' | grep .
find /usr/lib/modules -name '9pnet_virtio.ko*' | grep .
dnf clean all
rm -f /etc/ssh/ssh_host_*
cloud-init clean --logs --machine-id
sync
rm -f /usr/local/sbin/flightctl-prepare-guest
echo GUEST_PREPARATION_OK > /dev/ttyS0
'''
# Keep provisioning outside cloud-init's cgroup so upgrading cloud-init does
# not terminate the transaction that is preparing the image.
config = {
    "write_files": [{"path": "/usr/local/sbin/flightctl-prepare-guest", "permissions": "0755", "content": provision}],
    "runcmd": [["systemd-run", "--unit=flightctl-guest-preparation", "--collect",
                "--property=StandardOutput=journal+console", "--property=StandardError=journal+console",
                "/usr/local/sbin/flightctl-prepare-guest"]],
}
(work / 'user-data').write_text('#cloud-config\n' + json.dumps(config) + '\n')
(work / 'meta-data').write_text('instance-id: flightctl-guest-preparation\n')
(work / 'source.json').write_text(json.dumps({"url": sys.argv[2], "sha256": sys.argv[3]}, indent=2) + '\n')
PY

"${iso_command[@]}" -quiet -output "$work_dir/seed.iso" -volid cidata -joliet -rock "$work_dir/user-data" "$work_dir/meta-data"
qemu-img create -q -f qcow2 -F qcow2 -b "$(realpath "$source_image")" "$work_dir/overlay.qcow2"
qemu-img resize -q "$work_dir/overlay.qcow2" 16G
accel=tcg
cpu=max
if [[ $(uname -m) == x86_64 && -r /dev/kvm && -w /dev/kvm && ${VM_GUEST_ACCELERATOR:-kvm} == kvm ]]; then
    accel=kvm
    cpu=host
fi
# No host mounts or elevated capabilities: 9p only exports this build's manifest.
timeout --foreground --kill-after=30s "${VM_GUEST_PREPARATION_TIMEOUT:-45m}" \
    qemu-system-x86_64 -accel "$accel" -cpu "$cpu" -m 4096 -smp 4 \
    -drive "file=$work_dir/overlay.qcow2,if=virtio,format=qcow2" \
    -drive "file=$work_dir/seed.iso,if=virtio,format=raw,readonly=on" \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0 \
    -virtfs "local,path=$work_dir/share,mount_tag=hostshare,security_model=none" \
    -display none -serial "file:$work_dir/console.log"
if ! grep -q '^GUEST_PREPARATION_OK' "$work_dir/console.log"; then
    cat "$work_dir/console.log" >&2
    echo 'Guest preparation failed; output disk was not published.' >&2
    exit 1
fi
qemu-img check "$work_dir/overlay.qcow2"
qemu-img convert -O qcow2 "$work_dir/overlay.qcow2" "$output_dir/guest.disk.new"
mv "$output_dir/guest.disk.new" "$output_dir/guest.disk"
cp "$work_dir/share/guest.rpms" "$output_dir/guest.rpms"
cp "$work_dir/source.json" "$output_dir/guest-source.json"
(cd "$output_dir" && sha256sum guest.disk > guest.sha256)
chmod 0644 "$output_dir/guest.disk" "$output_dir/guest.rpms" "$output_dir/guest.sha256"
printf 'Prepared guest: %s\n' "$output_dir/guest.disk"
cat "$output_dir/guest.sha256"

# Packaging artifacts for portable media

After downloading Flight Control artifacts (RPM packages, container images, or a bundle
archive) on a connected prep machine, you must transfer them to an air-gapped target
machine using portable media or another transfer method.

This document covers the most common transfer formats in full detail, and briefly
describes additional options for specific scenarios.

## Tar archives

A tar archive is the simplest and most universal transfer format. The `flightctl-mirror-images --bundle`
command produces a `.tar.gz` archive directly. For RPM packages or other files, you
create the archive manually.

### Creating a tar archive

```bash
tar -czf ~/flightctl-artifacts.tar.gz -C ~ flightctl-rpms/
```

This creates `flightctl-artifacts.tar.gz` containing all files from `~/flightctl-rpms/`.
Using `-C ~` and a relative path keeps the archive paths clean (no leading `/home/user/`).

Verify the archive contents before transfer:

```bash
tar -tzf ~/flightctl-artifacts.tar.gz | head -20
```

### Transferring and extracting

Transfer the archive using `scp` (if the target is reachable via a jump host) or
copy it to physical media:

```bash
scp ~/flightctl-artifacts.tar.gz <user>@<target_host>:~/
```

On the target machine, extract the archive:

```bash
mkdir -p ~/flightctl-rpms
tar -xzf ~/flightctl-artifacts.tar.gz -C ~/
```

## USB drives

A USB drive is the standard physical media format for sites where no network path
exists between the prep machine and the target. Two filesystem formats are supported:

- **ext4** — Linux-native; best performance on Linux hosts; not readable on Windows or macOS
  without additional software.
- **exFAT** — Cross-platform; readable on Linux, Windows, and macOS without additional
  drivers; supports files larger than 4 GB (unlike FAT32).

Choose ext4 for Linux-only environments and exFAT when the USB drive must be readable
on multiple operating systems.

### Formatting with ext4

> [!WARNING]
> Formatting erases all data on the drive. Verify the device path with `lsblk` before
> running `mkfs` to avoid overwriting the wrong disk.

1. Identify the USB device path:

   ```bash
   lsblk
   ```

   ```console
   NAME   MAJ:MIN RM   SIZE RO TYPE MOUNTPOINTS
   sda      8:0    0  20.0G  0 disk
   sdb      8:16   1   8.0G  0 disk
   └─sdb1   8:17   1   8.0G  0 part
   ```

   In this example, the USB drive is `/dev/sdb`.

2. Create an ext4 filesystem:

   ```bash
   sudo mkfs.ext4 -L flightctl /dev/sdb1
   ```

3. Mount the drive:

   ```bash
   sudo mkdir -p /mnt/usb
   sudo mount /dev/sdb1 /mnt/usb
   ```

4. Copy the artifacts:

   ```bash
   cp ~/flightctl-artifacts.tar.gz /mnt/usb/
   ```

5. Unmount safely before removing the drive:

   ```bash
   sudo umount /mnt/usb
   ```

### Formatting with exFAT

> [!WARNING]
> Formatting erases all data on the drive. Verify the device path with `lsblk` before
> proceeding.

1. Install exFAT tools if not already present:

   ```bash
   sudo dnf install -y exfatprogs
   ```

2. Create an exFAT filesystem:

   ```bash
   sudo mkfs.exfat -n flightctl /dev/sdb1
   ```

3. Mount the drive:

   ```bash
   sudo mkdir -p /mnt/usb
   sudo mount /dev/sdb1 /mnt/usb
   ```

4. Copy the artifacts and unmount:

   ```bash
   cp ~/flightctl-artifacts.tar.gz /mnt/usb/
   sudo umount /mnt/usb
   ```

### Mounting and reading the USB drive on the target machine

1. Insert the USB drive. Identify the device path:

   ```bash
   lsblk
   ```

2. Mount the drive:

   ```bash
   sudo mkdir -p /mnt/usb
   sudo mount /dev/sdb1 /mnt/usb
   ```

3. Copy the archive to the local home directory and extract:

   ```bash
   cp /mnt/usb/flightctl-artifacts.tar.gz ~/
   tar -xzf ~/flightctl-artifacts.tar.gz -C ~/
   ```

4. Unmount the drive:

   ```bash
   sudo umount /mnt/usb
   ```

## Additional formats

### ISO images

ISO images are useful for read-only media or compliance scenarios where an immutable
archive is required. Create an ISO from a directory using `xorriso`:

```bash
xorriso -as mkisofs -o flightctl-artifacts.iso ~/flightctl-rpms/
```

Mount on the target with `sudo mount -o loop flightctl-artifacts.iso /mnt/iso`.

### Network shares

For sites with a restricted local network between the prep machine and the target,
a temporary HTTP file server is the simplest option. On the prep machine:

```bash
python3 -m http.server 8080 --directory ~/
```

On the target, download the archive:

```bash
curl -O http://<prep_machine_ip>:8080/flightctl-artifacts.tar.gz
```

Stop the server on the prep machine when the transfer is complete.

## OpenShift artifacts

Transferring artifacts for a disconnected OpenShift installation involves container
images, the Helm chart, and optionally OS images for the image builder. This section
covers the OCP-specific considerations.

### Size estimates

| Artifact set | Approximate size | Recommended media |
|---|---|---|
| Community variant images (`community-el9` or `community-el10`) | 5–8 GB | 16 GB+ USB or network share |
| Red Hat variant images (`rhem-el9` or `rhem-el10`) | 10–15 GB | 32 GB+ USB or network share |
| Helm chart only (`flightctl-<version>.tgz`) | < 1 MB | Any |
| Image builder service images (podman, bootc-image-builder, syft) | ~3 GB additional | Add to above |

> [!WARNING]
> FAT32 has a 4 GB maximum file size. The community variant bundle
> fits within this limit as individual images but the Red Hat variant may produce
> files exceeding 4 GB. Format USB drives with **exFAT** or **ext4** — see
> [USB drives](#usb-drives) above.

### Transferring the bundle archive

The `flightctl-mirror-images --bundle` command produces a single `.tar.gz` archive
containing all images and an `import.sh` script. This is the recommended format for
OCP transfers — transfer the archive to any machine inside the disconnected
environment that can reach the internal mirror registry, then run `import.sh`:

```bash
# On a machine with registry access inside the disconnected environment
mkdir ~/flightctl-bundle
tar -xzf ~/flightctl-bundle.tar.gz -C ~/flightctl-bundle
cd ~/flightctl-bundle
./import.sh --registry <internal-mirror-registry-host>:<port>
```

### Checksum verification

Generate a checksum on the prep machine before transfer:

```bash
sha256sum ~/flightctl-bundle.tar.gz > ~/flightctl-bundle.tar.gz.sha256
```

Transfer the `.sha256` file alongside the archive. Verify on the target machine
before extracting:

```bash
sha256sum --check ~/flightctl-bundle.tar.gz.sha256
```

A `OK` result confirms the archive arrived intact. Do not proceed with import if
verification fails — re-transfer the archive.

### Splitting large archives

If the archive exceeds the capacity of your transfer media, split it into smaller
chunks:

```bash
# Split into 3 GB chunks
split -b 3G ~/flightctl-bundle.tar.gz ~/flightctl-bundle.tar.gz.part-

# List the parts
ls ~/flightctl-bundle.tar.gz.part-*
```

Transfer all parts. Reassemble on the target before extraction:

```bash
cat ~/flightctl-bundle.tar.gz.part-* > ~/flightctl-bundle.tar.gz
sha256sum --check ~/flightctl-bundle.tar.gz.sha256
tar -xzf ~/flightctl-bundle.tar.gz -C ~/flightctl-bundle
```

### Transferring the Helm chart

Download the Helm chart on the prep machine:

```bash
helm pull oci://quay.io/flightctl/charts/flightctl --version <version>
# Produces: flightctl-<version>.tgz
```

Transfer the `.tgz` file using any of the methods above. The chart is small (< 1 MB)
and does not require splitting.

### Network share deployment

For sites with a restricted local network, serve the bundle from a temporary HTTP
server on a bastion host that can reach both the prep machine and the internal
mirror registry:

```bash
# On the bastion host
python3 -m http.server 8080 --directory ~/
```

From the target machine (or the machine with registry access):

```bash
curl -O http://<bastion_ip>:8080/flightctl-bundle.tar.gz
curl -O http://<bastion_ip>:8080/flightctl-bundle.tar.gz.sha256
sha256sum --check flightctl-bundle.tar.gz.sha256
```

## Next steps

- [Installing Flight Control in a disconnected OpenShift cluster](installing-service-on-openshift-disconnected.md) —
  end-to-end OCP disconnected installation procedure
- [Setting up a local RPM repository](offline-rpm-repository.md) — how to prepare
  packages before packaging them for transfer
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md) —
  end-to-end installation procedure on the target machine

# Packaging artifacts for portable media

After downloading Flight Control artifacts (RPM packages, container images, or a bundle
archive) on a connected prep machine, you must transfer them to an air-gapped target
machine using portable media or another transfer method.

This document covers the most common transfer formats in full detail, and briefly
describes additional options for specific scenarios.

## Tar archives

A tar archive is the simplest and most universal transfer format. The `mirror-images --bundle`
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

## Next steps

- [Setting up a local RPM repository](offline-rpm-repository.md) — how to prepare
  packages before packaging them for transfer
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md) —
  end-to-end installation procedure on the target machine

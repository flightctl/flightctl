#!/bin/sh
set -e

mkdir -p /tmp/mount
sudo LIBGUESTFS_BACKEND=direct guestmount -a bin/output/qcow2/disk.qcow2 -m /dev/sda4:/ -m /dev/sda3:/boot -m /dev/sda2:/boot/efi -o allow_other /tmp/mount
trap "sudo guestunmount /tmp/mount" ERR SIGINT

PCR=0000000000000000000000000000000000000000000000000000000000000000


pcr_measure() {
    
    SHA_FILE=$(openssl dgst -sha256 -binary $1 | xxd -p -c 32)
    echo Measuring $1 = $SHA_FILE
    echo Old PCR: $PCR
    PCR=$(echo "${PCR}${SHA_FILE}" | xxd -r -p | openssl dgst -binary -sha256 | xxd -u -p -c 32)
    echo New PCR: $PCR
    echo "---"
}

pcr_measure /tmp/mount/boot/efi/EFI/centos/grub.cfg
pcr_measure /tmp/mount/boot/efi/EFI/centos/grub.cfg
pcr_measure /tmp/mount/boot/efi/EFI/centos/bootuuid.cfg
pcr_measure /tmp/mount/boot/grub2/grub.cfg
pcr_measure /tmp/mount/boot/grub2/bootuuid.cfg
pcr_measure /tmp/mount/boot/grub2/grubenv
pcr_measure /tmp/mount/boot/loader/entries//ostree-1.conf
pcr_measure /tmp/mount/boot/ostree/default-174d06c7fdfd3ef3dba3c750ed90fb15cadf899db7800bc4330d17e69aaef584/vmlinuz-5.14.0-432.el9.x86_64
pcr_measure /tmp/mount/boot/ostree/default-174d06c7fdfd3ef3dba3c750ed90fb15cadf899db7800bc4330d17e69aaef584/initramfs-5.14.0-432.el9.x86_64.img

sudo guestunmount /tmp/mount
#!/bin/bash

# Script to run the VM demo test with a real disk image
# This will actually create a VM, take a snapshot, revert to it, and clean up

set -e

echo "🔄 VM Demo Test Runner"
echo "======================"

# Check if libvirt is available
if ! command -v virsh &> /dev/null; then
    echo "❌ Error: virsh command not found. Please install libvirt."
    exit 1
fi

# Check if libvirt daemon is running
if ! virsh list &> /dev/null; then
    echo "❌ Error: Cannot connect to libvirt. Please ensure libvirtd is running."
    echo "   Try: sudo systemctl start libvirtd"
    exit 1
fi

echo "✅ Libvirt connection verified"

# Check if disk image path is provided
if [ -z "$TEST_DISK_IMAGE_PATH" ]; then
    echo "❌ Error: TEST_DISK_IMAGE_PATH not set"
    echo ""
    echo "Please set TEST_DISK_IMAGE_PATH to a valid QCOW2 disk image path"
    echo "Example:"
    echo "  export TEST_DISK_IMAGE_PATH=/path/to/your/disk.qcow2"
    echo "  ./run_demo.sh"
    echo ""
    echo "You can find disk images in:"
    echo "  - /var/lib/libvirt/images/"
    echo "  - Or create one with: qemu-img create -f qcow2 disk.qcow2 10G"
    exit 1
fi

# Check if the disk image exists
if [ ! -f "$TEST_DISK_IMAGE_PATH" ]; then
    echo "❌ Error: Disk image does not exist: $TEST_DISK_IMAGE_PATH"
    exit 1
fi

echo "✅ Disk image found: $TEST_DISK_IMAGE_PATH"

# Enable integration tests
export SKIP_LIBVIRT_TESTS=0

echo ""
echo "🔄 Running VM Demo Test..."
echo "   This will:"
echo "   1. Create a VM"
echo "   2. Start the VM"
echo "   3. Create a snapshot"
echo "   4. Revert to the snapshot"
echo "   5. Delete the snapshot"
echo "   6. Clean up the VM"
echo ""

# Run the demo test
go test -v ./test/harness/e2e/vm/ -run TestVMDemo -ginkgo.v

echo ""
echo "✅ VM Demo Test completed!" 
# VM Snapshot Tests

This directory contains unit tests for the VM snapshot functionality in `vm_linux.go`.

## Test Files

- `vm_linux_test.go` - Unit tests for VM creation and snapshot functionality
- `vm_snapshot_integration_test.go` - Integration tests demonstrating the full snapshot workflow

## Running the Tests

### Unit Tests (Default)
```bash
# Run all VM tests (unit tests only)
go test -v ./test/harness/e2e/vm/

# Run specific test suite
go test -v ./test/harness/e2e/vm/ -run TestVMLinux
```

### Integration Tests (Requires Real Libvirt Environment)

The integration tests are skipped by default as they require a real libvirt environment with a valid disk image.

To run the integration tests:

1. **Set up libvirt environment:**
   ```bash
   # Ensure libvirt is running
   sudo systemctl start libvirtd
   
   # Add your user to the libvirt group
   sudo usermod -a -G libvirt $USER
   ```

2. **Provide a disk image:**
   ```bash
   # Set the path to a valid QCOW2 disk image
   export TEST_DISK_IMAGE_PATH="/path/to/your/disk.qcow2"
   ```

3. **Run the integration tests:**
   ```bash
   # Enable integration tests
   export SKIP_LIBVIRT_TESTS=0
   
   # Run the tests
   go test -v ./test/harness/e2e/vm/ -ginkgo.focus "Integration"
   ```

## Test Workflow

The integration tests demonstrate the complete snapshot workflow:

1. **Create VM** - Initialize a new VM instance
2. **Create Domain** - Define the VM in libvirt
3. **Start VM** - Boot the virtual machine
4. **Create Snapshot** - Take an external snapshot with memory state
5. **Verify Snapshot** - Confirm the snapshot exists
6. **Revert to Snapshot** - Restore the VM to the snapshot state
7. **Verify Running State** - Ensure VM is still running after revert
8. **Delete Snapshot** - Clean up the snapshot
9. **Cleanup VM** - Remove the VM and all resources

## External Snapshots

The tests verify that the VM uses external snapshots with memory state, which is equivalent to:

```bash
# Creating external snapshot
virsh snapshot-create-as --domain <vm-name> --name <snapshot-name> --memspec file=<memory-file>

# Reverting to snapshot with running state
virsh snapshot-revert --domain <vm-name> <snapshot-name> --running
```

## Memory File Path

The VM automatically sets a default memory file path if not provided:
- Default: `{TestDir}/{VMName}.mem`
- Custom: Can be set via `MemoryFilePath` field in `TestVM` struct

## Test Coverage

### Unit Tests
- VM creation with default memory file path
- VM creation with custom memory file path
- Memory file path handling (empty vs custom)
- Method signature verification
- Interface compliance

### Integration Tests
- Full snapshot workflow (create → snapshot → revert → cleanup)
- Multiple snapshot handling
- VM state verification
- Resource cleanup

## Notes

- Unit tests run without requiring libvirt and are suitable for CI/CD
- Integration tests require a real libvirt environment and are meant for manual testing
- All tests use temporary directories for isolation
- Tests verify both the external snapshot functionality and the `--running` flag equivalent

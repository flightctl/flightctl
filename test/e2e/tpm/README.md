# TPM E2E Tests

This directory contains End-to-End tests for TPM (Trusted Platform Module) device authentication and attestation functionality.

## Overview

The TPM tests validate that devices can:
- Enroll using TPM-based cryptographic identity
- Generate and submit TPM attestation data
- Perform integrity verification using TPM hardware
- Maintain secure communication using TPM-signed credentials

## Environment Configuration

The test behavior automatically adjusts based on the TPM hardware type:

### Virtual TPM (Default)
```bash
# Test with virtual TPM (software TPM in VMs)
go test ./test/e2e/tpm
# or explicitly:
FLIGHTCTL_REAL_TPM=false go test ./test/e2e/tpm
```

**Expected Results:**
- TPM functionality works correctly (enrollment, attestation, communication)
- Integrity verification shows "Failed" status (expected due to lack of chain of trust)
- Test validates that all TPM operations work despite verification failure

### Real Hardware TPM
```bash
# Test with real hardware TPM devices
FLIGHTCTL_REAL_TPM=true go test ./test/e2e/tpm
```

**Expected Results:**
- TPM functionality works correctly (enrollment, attestation, communication)  
- Integrity verification shows "Verified" status (full chain of trust validation)
- Test validates complete TPM security chain

## Test Structure

The test suite includes:

1. **TPM Device Detection** - Verifies `/dev/tpm0` presence and accessibility
2. **TPM Functionality Check** - Tests basic TPM operations (`tpm2_startup`, `tpm2_getrandom`)
3. **Agent Configuration** - Configures FlightCtl agent to use TPM for device identity
4. **Enrollment Process** - Validates TPM-based device enrollment with attestation data
5. **Integrity Verification** - Checks TPM-based integrity verification status
6. **Ongoing Operations** - Verifies continued TPM usage for device communication

## Key Features

- **Automatic Hardware Detection**: Test assertions adapt based on `FLIGHTCTL_REAL_TPM` environment variable
- **Comprehensive Logging**: Detailed logs help debug TPM setup and operation issues
- **Reusable Functions**: TPM setup functions in `harness_device.go` can be used by other test suites
- **Production Ready**: Clean output with minimal debugging noise in normal operation

## Dependencies

- TPM 2.0 hardware (real or virtual)
- `tpm2-tools` package for TPM utilities
- FlightCtl agent with TPM support compiled in
- VM or hardware with TPM device accessible as `/dev/tpm0`

## Troubleshooting

### Virtual TPM Issues
- Ensure VM has virtual TPM enabled
- Verify `/dev/tpm0` device exists
- Check that `tpm2-tools` are installed

### Real TPM Issues  
- Verify TPM is enabled in BIOS/UEFI
- Check TPM ownership and clear state if needed
- Ensure proper permissions on `/dev/tpm0`
- Validate TPM certificates and chain of trust

### Common Problems
- **"TPM device identity is disabled"**: Check that agent config contains TPM section
- **"Using file-based identity provider"**: Agent not reading TPM config or TPM initialization failed
- **Integrity verification stuck**: TPM attestation process may be taking longer than expected

For detailed debugging, check agent logs:
```bash
sudo journalctl -u flightctl-agent -f
```

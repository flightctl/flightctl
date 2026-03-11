# TPM E2E Tests

This directory contains End-to-End tests for TPM (Trusted Platform Module) device authentication and attestation functionality.

## Overview

The TPM tests validate that devices can:
- Enroll using TPM-based cryptographic identity
- Generate and submit TPM attestation data
- Perform integrity verification using TPM hardware
- Maintain secure communication using TPM-signed credentials

## Test Labels

| Label | Description |
|-------|-------------|
| `tpm` | All TPM tests |
| `tpm-sw` | Software TPM (swtpm) tests only |
| `tpm-real` | Real hardware TPM passthrough tests only |
| `sanity` | Included in sanity test runs |

## Running Tests

### Software TPM (swtpm)

The swtpm tests run against VMs from the pool that use a software TPM emulator.
No special host setup is required.

```bash
# Run only swtpm tests
GINKGO_LABEL_FILTER=tpm-sw go test ./test/e2e/tpm/...
```

### Real Hardware TPM

The real TPM tests pass the host's `/dev/tpm0` device into a QEMU VM via
libvirt TPM passthrough. The test is automatically skipped if `/dev/tpm0`
is not present on the host.

> **Important: `/dev/tpm0` vs `/dev/tpmrm0` for passthrough**
>
> The passthrough backend **must** use the raw TPM device (`/dev/tpm0`), not
> the kernel resource manager device (`/dev/tpmrm0`). Passing through
> `/dev/tpmrm0` creates a double resource manager (host RM + guest RM) which
> is a [known unsupported configuration](https://access.redhat.com/solutions/6986904).
>
> Using `/dev/tpm0` requires **exclusive access** — no other process on the
> host (e.g. `tpm2-abrmd`) can hold the device open while the VM is running.

#### Host Setup

1. Ensure no other process is using the TPM:
   ```bash
   sudo systemctl stop tpm2-abrmd 2>/dev/null || true
   sudo fuser /dev/tpm0  # should return nothing
   ```
2. Add a udev rule to allow group access to `/dev/tpm0`. By default, the udev
   rules only grant access to the `tss` **user**, not group members. Create a
   custom rule:
   ```bash
   echo 'KERNEL=="tpm[0-9]*", MODE="0660", OWNER="tss", GROUP="tss"' | sudo tee /etc/udev/rules.d/99-tpm-group.rules
   sudo udevadm control --reload-rules
   sudo udevadm trigger /dev/tpm0
   ```
3. Add your user to the `tss` group:
   ```bash
   sudo usermod -aG tss $USER
   ```
4. Apply the group change. On some desktop environments (e.g. GNOME on Fedora),
   logging out and back in may not be sufficient. Use `newgrp` in the shell where
   you will run the tests:
   ```bash
   newgrp tss
   ```
5. Verify access:
   ```bash
   ls -l /dev/tpm0  # should show tss:tss
   cat < /dev/tpm0  # should hang (Ctrl+C), not "Permission denied"
   ```

#### Running

```bash
# Run only real TPM tests
GINKGO_LABEL_FILTER=tpm-real go test ./test/e2e/tpm/...
```

### All TPM Tests

```bash
# Run both swtpm and real TPM tests
GINKGO_LABEL_FILTER=tpm go test ./test/e2e/tpm/...
```

## Test Structure

The test suite includes:

1. **TPM Agent Configuration** - Configures the FlightCtl agent to use TPM for device identity
2. **Enrollment with Attestation** - Validates TPM-based device enrollment with TCG CSR format
3. **TPM Challenge-Response** - Verifies the TPMVerified condition is set on the EnrollmentRequest
4. **Integrity Verification** - Checks TPM-based integrity verification status on the Device
5. **Ongoing Operations** - Verifies continued TPM usage for device communication (config delivery)

## Dependencies

- TPM 2.0 hardware (real) or libvirt with swtpm (software)
- `tpm2-tools` package installed in the VM image
- FlightCtl agent with TPM support
- TPM manufacturer CA certificates in `tpm-manufacturer-certs/` (for real TPM tests)

## Troubleshooting

### Common Problems
- **"TPM device identity is disabled"**: Agent config does not contain the TPM section
- **"Using file-based identity provider"**: Agent started before TPM config was written; ensure the agent is restarted after configuration
- **"Using persisted CSR for enrollment"**: A stale CSR from a previous boot exists at `/var/lib/flightctl/certs/agent.csr`; clean identity files before restarting
- **TPM_RC_UNBALANCED or vendor errors through passthrough**: Likely caused by passing through `/dev/tpmrm0` instead of `/dev/tpm0` (see note above)
- **Permission denied on /dev/tpm0**: User is not in the `tss` group (see Host Setup above)

### Debugging

Check agent logs inside the VM:
```bash
sudo journalctl -u flightctl-agent --no-pager
```

Check API server logs for TPM challenge errors:
```bash
kubectl logs -n flightctl-external -l app.kubernetes.io/name=flightctl-api --tail=100
```

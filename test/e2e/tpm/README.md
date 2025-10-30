# TPM E2E Tests - Real Hardware

This directory contains End-to-End tests for **real hardware TPM (Trusted Platform Module) 2.0** device authentication and attestation functionality.

## Overview

The TPM test validates the **complete TPM verification workflow** on real hardware, including:

- ✅ TPM 2.0 hardware detection and access
- ✅ TPM manufacturer identification from EK certificate
- ✅ TPM CA certificate chain configuration
- ✅ FlightCtl agent installation from Copr repository
- ✅ TPM key generation (EK, LAK, LDevID)
- ✅ TCG-compliant CSR creation with attestation data
- ✅ EK certificate chain validation
- ✅ Credential challenge protocol (Section 6.6.2.6 of TCG spec)
- ✅ Enrollment approval workflow
- ✅ TPM integrity verification (expects "Verified" status)
- ✅ Device identity verification
- ✅ TPM key persistence across reboots
- ✅ TPM-signed device communication

## Prerequisites

### Hardware Requirements
- **RHEL 9** hypervisor or bare metal system
- **TPM 2.0** hardware module (discrete TPM or fTPM)
- TPM enabled in BIOS/UEFI firmware
- Secure Boot recommended (optional)

### Software Requirements
```bash
# Install required packages
sudo dnf install -y tpm2-tools openssl golang

# Verify TPM device
ls -la /dev/tpm0

# Test TPM access
sudo tpm2_startup -c
sudo tpm2_getrandom 32 --hex
```

### Network Requirements
- Access to FlightCtl API server
- Access to Copr repository (https://copr.fedorainfracloud.org)
- HTTP/2 and gRPC support (for credential challenge)

### FlightCtl Server Configuration

The FlightCtl API server must be configured with TPM manufacturer CA certificates. See [docs/user/tpm-authentication.md](../../../docs/user/tpm-authentication.md) for detailed configuration instructions.

**Quick Setup:**

```bash
# 1. Copy TPM manufacturer certificates to server
sudo mkdir -p /etc/flightctl/tpm-cas
sudo cp tpm-manufacturer-certs/<manufacturer>/*.pem /etc/flightctl/tpm-cas/
sudo chmod 644 /etc/flightctl/tpm-cas/*.pem

# 2. Configure API server (add to config.yaml)
cat >> /etc/flightctl/config.yaml <<EOF
service:
  tpmCAPaths:
    - /etc/flightctl/tpm-cas/*.pem
EOF

# 3. Restart API server
sudo systemctl restart flightctl-api
```

## Running the Test

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `FLIGHTCTL_API_URL` | **Yes** | - | FlightCtl API server URL (e.g., `https://api.flightctl.example.com`) |
| `FLIGHTCTL_TPM_CA_DIR` | No | `/etc/flightctl/tpm-cas` | Directory containing TPM manufacturer CA certificates |
| `FLIGHTCTL_AGENT_COPR_REPO` | No | `https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev` | Copr repository URL |

### Run Test on RHEL9 Hypervisor

```bash
# Navigate to test directory
cd test/e2e/tpm

# Run with sudo (TPM access requires root)
sudo FLIGHTCTL_API_URL=https://api.flightctl.example.com \
     go test -v -timeout 30m

# Run with custom TPM CA directory
sudo FLIGHTCTL_API_URL=https://api.flightctl.example.com \
     FLIGHTCTL_TPM_CA_DIR=/opt/tpm-certs \
     go test -v -timeout 30m
```

### Test Workflow

The test performs the following steps automatically:

1. **Hardware Verification** (Step 1)
   - Detects TPM 2.0 device at `/dev/tpm0`
   - Verifies `tpm2-tools` installation
   - Tests TPM access and functionality

2. **TPM Manufacturer Detection** (Step 2)
   - Reads EK certificate from TPM NVRAM (index 0x01c00002)
   - Identifies manufacturer (Infineon, STMicroelectronics, Nuvoton, NSING)
   - Extracts certificate chain information

3. **CA Certificate Configuration** (Step 3)
   - Locates manufacturer CA certificates in repository
   - Copies certificates to system CA directory
   - Validates certificate chain structure

4. **Agent Installation** (Step 4)
   - Enables Copr repository: `@redhat-et/flightctl-dev`
   - Installs latest `flightctl` package via DNF
   - Verifies agent binary and version

5. **Agent Configuration** (Step 5)
   - Creates `/etc/flightctl/config.yaml` with TPM enabled
   - Configures API server URL
   - Sets TPM device path and debug logging

6. **Service Startup** (Step 6)
   - Enables and starts `flightctl-agent.service`
   - Waits for service to become active
   - Verifies TPM identity provider initialization

7. **Enrollment Request** (Step 7)
   - Waits for enrollment request creation
   - Extracts enrollment ID from agent logs or API
   - Validates request contains device information

8. **Attestation Verification** (Step 8)
   - Checks TPM attestation data in enrollment request
   - Verifies SystemInfo contains TPM-specific fields
   - Validates attestation data structure

9. **Credential Challenge** (Step 9)
   - Waits for credential challenge completion
   - Verifies agent solved the cryptographic challenge
   - Confirms enrollment request marked as "Verified"

10. **Enrollment Approval** (Step 10)
    - Approves the enrollment request via API
    - Waits for device creation
    - Extracts device ID

11. **Device Online Status** (Step 11)
    - Waits for device "Online" condition
    - Verifies device heartbeat
    - Confirms agent-server communication

12. **Integrity Verification** (Step 12)
    - Checks TPM integrity status: **Verified** ✅
    - Checks device identity status: **Verified** ✅
    - Checks overall integrity status: **Verified** ✅

13. **Key Persistence** (Step 13)
    - Verifies TPM blob file at `/var/lib/flightctl/tpm-blob.yaml`
    - Confirms TPM keys remain accessible

14. **TPM Communication** (Step 14)
    - Validates device uses TPM identity for API calls
    - Checks recent heartbeat timestamps
    - Confirms TPM-signed communication works

15. **Summary Report** (Step 15)
    - Prints comprehensive test results
    - Lists all verified components
    - Confirms security validation

## Expected Output

### Successful Test Run

```
🔧 Using FlightCtl API: https://api.flightctl.example.com
✅ Test setup completed

Step 1: Verifying TPM hardware prerequisites
🔍 Verifying TPM 2.0 hardware presence...
  ✅ TPM device found: /dev/tpm0
  ✅ tpm2-tools installed
  ✅ TPM accessible and responding
  ✅ TPM 2.0 verified

Step 2: Detecting TPM manufacturer and extracting EK certificate
🔍 Detecting TPM manufacturer from EK certificate...
  ✅ EK certificate found at index 0x01c00002
  ✅ Detected manufacturer: STMicroelectronics
  📄 EK Certificate: /tmp/ek_cert_01c00002.pem

Step 3: Verifying TPM CA certificates are configured
🔍 Verifying TPM CA certificates configuration...
  ✅ Found STMicroelectronics CA certificates in repository
    📄 Copied: st-micro-root.pem -> /etc/flightctl/tpm-cas/st-micro-root.pem
    📄 Copied: st-micro-intermediate.pem -> /etc/flightctl/tpm-cas/st-micro-intermediate.pem
  ✅ Total CA certificates configured: 2

[... Steps 4-14 output ...]

Step 15: Final verification - All TPM checks passed

═══════════════════════════════════════════════════════════════
✅ TPM VERIFICATION TEST PASSED - ALL CHECKS SUCCESSFUL
═══════════════════════════════════════════════════════════════

📋 Test Summary:
  • Device ID: device-123abc
  • Enrollment Request ID: er-456def
  • TPM Manufacturer: STMicroelectronics

✅ Verified Components:
  • TPM 2.0 hardware detection
  • TPM manufacturer identification
  • TPM CA certificate chain configuration
  • FlightCtl agent installation from Copr
  [... complete list ...]

🔐 Security Validation:
  • Hardware root of trust established
  • Certificate chain validated from device to manufacturer
  • Cryptographic proof of possession verified
  • Secure communication channel established

═══════════════════════════════════════════════════════════════

PASS
ok  	github.com/flightctl/flightctl/test/e2e/tpm	180.456s
```

## Supported TPM Manufacturers

The test automatically detects and configures certificates for:

- ✅ **Infineon** - [Product Page](https://www.infineon.com/products/security-smart-card-solutions/optiga-embedded-security-solutions/optiga-tpm)
- ✅ **STMicroelectronics** - [TN1330](https://www.st.com/resource/en/technical_note/tn1330-st-trusted-platform-module-tpm-endorsement-key-ek-certificates-stmicroelectronics.pdf)
- ✅ **Nuvoton** - [Certificate Chain Rev 2.2](https://www.nuvoton.com/export/sites/nuvoton/files/security/Nuvoton_TPM_EK_Certificate_Chain_Rev2.2.pdf)
- ✅ **NSING Technologies** - [Certificate Rev 1](https://nsing.com.sg/uploads/NSINGTPMEKcertificatesv1.0.pdf)

CA certificates are included in the `tpm-manufacturer-certs/` directory at the repository root.

## Troubleshooting

### TPM Device Not Found
```bash
# Check if TPM exists
ls -la /dev/tpm*

# Enable TPM in BIOS/UEFI if not present
# Reboot required after BIOS change
```

### TPM Access Denied
```bash
# Check permissions
ls -la /dev/tpm0

# Run test with sudo
sudo -E go test -v

# Check TPM ownership (may need to clear TPM in BIOS)
sudo tpm2_getcap properties-fixed
```

### Agent Installation Fails
```bash
# Enable Copr repo manually
sudo dnf copr enable -y @redhat-et/flightctl-dev

# Check repository accessibility
sudo dnf repolist

# Install agent manually
sudo dnf install -y flightctl
```

### Certificate Chain Validation Fails

```bash
# Verify TPM manufacturer is supported
sudo tpm2_nvread 0x01c00002 -o /tmp/ek.der
openssl x509 -inform DER -in /tmp/ek.der -text -noout | grep Issuer

# Check CA certificates are present
ls -la /etc/flightctl/tpm-cas/

# Manually copy manufacturer certificates
sudo cp tpm-manufacturer-certs/<manufacturer>/*.pem /etc/flightctl/tpm-cas/

# Restart FlightCtl API server
sudo systemctl restart flightctl-api
```

### Credential Challenge Timeout

```bash
# Check network connectivity to API server
curl -I $FLIGHTCTL_API_URL

# Verify gRPC/HTTP2 is accessible
# (credential challenge uses gRPC)

# Check agent logs
sudo journalctl -u flightctl-agent -f

# Check for firewall issues blocking gRPC
sudo firewall-cmd --list-all
```

### Integrity Verification Shows "Failed"

This indicates the certificate chain validation failed. Possible causes:

1. **Missing CA Certificates**: FlightCtl API server doesn't have manufacturer CAs
2. **Expired Certificates**: EK certificate or CA certificates expired
3. **Certificate Mismatch**: Wrong manufacturer CA certificates configured
4. **Invalid Chain**: Certificate chain broken or incomplete

**Resolution:**
```bash
# 1. Identify your TPM manufacturer
sudo tpm2_nvread 0x01c00002 -o /tmp/ek.der
openssl x509 -inform DER -in /tmp/ek.der -text -noout

# 2. Copy correct manufacturer certificates to API server
sudo cp tpm-manufacturer-certs/<manufacturer>/*.pem /etc/flightctl/tpm-cas/

# 3. Update API server configuration
sudo vim /etc/flightctl/config.yaml
# Add: tpmCAPaths: ["/etc/flightctl/tpm-cas/*.pem"]

# 4. Restart API server
sudo systemctl restart flightctl-api

# 5. Re-run test
```

### Agent Logs

```bash
# Real-time agent logs
sudo journalctl -u flightctl-agent -f

# Filter for TPM-related messages
sudo journalctl -u flightctl-agent | grep -i tpm

# Check for errors
sudo journalctl -u flightctl-agent | grep -i error

# Full logs since service start
sudo journalctl -u flightctl-agent --since "10 minutes ago" --no-pager
```

## Integration with CI/CD

For automated testing in CI/CD pipelines:

```bash
#!/bin/bash
# ci-tpm-test.sh

set -euo pipefail

# Prerequisites check
if [[ ! -e /dev/tpm0 ]]; then
    echo "❌ TPM device not found"
    exit 1
fi

# Run TPM test with timeout
sudo FLIGHTCTL_API_URL="${API_URL}" \
     FLIGHTCTL_TPM_CA_DIR="${TPM_CA_DIR:-/etc/flightctl/tpm-cas}" \
     go test -v -timeout 30m ./test/e2e/tpm \
     -ginkgo.label-filter="real-tpm" \
     -ginkgo.v

echo "✅ TPM test completed successfully"
```

## Additional Resources

- [TPM Authentication Documentation](../../../docs/user/tpm-authentication.md)
- [Agent Configuration Guide](../../../docs/user/configuring-agent.md)
- [TCG Specification - Device Identity](https://trustedcomputinggroup.org/wp-content/uploads/TPM-2.0-Keys-for-Device-Identity-and-Attestation-v1.10r9_pub.pdf)
- [TPM 2.0 Tools](https://tpm2-tools.readthedocs.io/)
- [FlightCtl Copr Repository](https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev/packages/)

## Security Considerations

### Hardware Root of Trust

This test validates that:
- Private keys never leave the TPM hardware
- Cryptographic operations are performed within TPM
- Certificate chain traces back to TPM manufacturer
- Proof of possession demonstrates live TPM ownership

### Certificate Validation

The complete certificate chain is verified:
```
TPM Manufacturer Root CA
    └── TPM Manufacturer Intermediate CA
            └── Endorsement Key (EK) Certificate
                    └── Device Identity (LDevID)
```

### Credential Challenge Protocol

The test verifies the TCG-specified credential challenge:
1. Service generates cryptographic secret
2. Secret encrypted with device's EK public key
3. Agent decrypts using TPM-bound private key
4. Decrypted secret proves TPM ownership
5. Challenge valid only for gRPC connection lifetime

This prevents:
- Replay attacks (ephemeral secrets)
- Man-in-the-middle attacks (gRPC TLS)
- Key extraction (TPM-bound keys)

## Contributing

When adding new TPM manufacturer support:

1. Add manufacturer CA certificates to `tpm-manufacturer-certs/<manufacturer>/`
2. Update `detectTPMManufacturer()` function with detection logic
3. Add manufacturer to supported list in README
4. Test with real hardware from that manufacturer

## License

See [LICENSE](../../../LICENSE) file in the repository root.

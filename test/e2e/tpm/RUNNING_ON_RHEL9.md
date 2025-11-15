# Running Real TPM E2E Test on RHEL9 Hypervisor

## Quick Start Guide

This guide walks you through running the comprehensive TPM E2E test on a RHEL9 hypervisor with real TPM 2.0 hardware.

## Prerequisites Setup

### 1. System Requirements

```bash
# Verify RHEL 9 system
cat /etc/redhat-release
# Expected: Red Hat Enterprise Linux release 9.x

# Verify TPM 2.0 hardware
ls -la /dev/tpm0
# Expected: crw-rw---- 1 tss tss 10, 224 Oct 30 10:00 /dev/tpm0
```

### 2. Install Required Packages

```bash
# Install TPM tools and dependencies
sudo dnf install -y tpm2-tools openssl golang git

# Verify installation
tpm2_startup -c
tpm2_getrandom 32 --hex
```

### 3. Clone FlightCtl Repository

```bash
# Clone the repository
cd $HOME
git clone https://github.com/flightctl/flightctl.git
cd flightctl

# Switch to your TPM branch if needed
git checkout tpm-new  # or your branch name
```

### 4. Configure FlightCtl API Server

The FlightCtl API server must be configured with TPM manufacturer CA certificates **before** running the test.

#### Option A: Using Kubernetes/Kind

```bash
# 1. Copy TPM CA certificates to ConfigMap
kubectl create configmap tpm-ca-certs \
  --from-file=tpm-manufacturer-certs/infineon/ \
  --from-file=tpm-manufacturer-certs/st-micro/ \
  --from-file=tpm-manufacturer-certs/nuvoton/ \
  --from-file=tpm-manufacturer-certs/nsing/ \
  --namespace=flightctl

# 2. Update API server configuration
kubectl edit configmap flightctl-api-config -n flightctl

# Add to service section:
# service:
#   tpmCAPaths:
#     - /etc/flightctl/tpm-cas/*.pem

# 3. Mount certificates in API deployment
kubectl patch deployment flightctl-api -n flightctl --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/volumes/-",
    "value": {
      "name": "tpm-ca-certs",
      "configMap": {"name": "tpm-ca-certs"}
    }
  },
  {
    "op": "add",
    "path": "/spec/template/spec/containers/0/volumeMounts/-",
    "value": {
      "name": "tpm-ca-certs",
      "mountPath": "/etc/flightctl/tpm-cas",
      "readOnly": true
    }
  }
]'

# 4. Wait for rollout
kubectl rollout status deployment/flightctl-api -n flightctl

# 5. Get API URL
export FLIGHTCTL_API_URL=$(kubectl get svc flightctl-api -n flightctl -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
echo "API URL: https://${FLIGHTCTL_API_URL}"
```

#### Option B: Standalone Deployment

```bash
# 1. Create TPM CA directory on API server host
sudo mkdir -p /etc/flightctl/tpm-cas

# 2. Copy all manufacturer certificates
sudo cp tpm-manufacturer-certs/infineon/*.pem /etc/flightctl/tpm-cas/
sudo cp tpm-manufacturer-certs/st-micro/*.pem /etc/flightctl/tpm-cas/
sudo cp tpm-manufacturer-certs/nuvoton/*.pem /etc/flightctl/tpm-cas/
sudo cp tpm-manufacturer-certs/nsing/*.pem /etc/flightctl/tpm-cas/

# 3. Set permissions
sudo chmod 644 /etc/flightctl/tpm-cas/*.pem

# 4. Update API server configuration
sudo vi /etc/flightctl/config.yaml

# Add:
# service:
#   tpmCAPaths:
#     - /etc/flightctl/tpm-cas/infineon-root-ca.pem
#     - /etc/flightctl/tpm-cas/infineon-intermediate-ca.pem
#     - /etc/flightctl/tpm-cas/st-micro-root-ca.pem
#     - /etc/flightctl/tpm-cas/st-micro-intermediate-ca.pem
#     - /etc/flightctl/tpm-cas/nuvoton-root-ca.pem
#     - /etc/flightctl/tpm-cas/nuvoton-intermediate-ca.pem
#     - /etc/flightctl/tpm-cas/nsing-root-ca.pem
#     - /etc/flightctl/tpm-cas/nsing-intermediate-ca.pem

# 5. Restart API server
sudo systemctl restart flightctl-api
```

## Running the Test

### 1. Set Environment Variables

```bash
# Required: Set FlightCtl API URL
export FLIGHTCTL_API_URL="https://api.flightctl.example.com"

# Optional: Set custom TPM CA directory (default: /etc/flightctl/tpm-cas)
export FLIGHTCTL_TPM_CA_DIR="/opt/custom/tpm-certs"

# Optional: Set custom Copr repository
export FLIGHTCTL_AGENT_COPR_REPO="https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev"
```

### 2. Navigate to Test Directory

```bash
cd test/e2e/tpm
```

### 3. Run the Test

```bash
# Run with sudo (TPM access requires root)
sudo -E FLIGHTCTL_API_URL="${FLIGHTCTL_API_URL}" \
     go test -v -timeout 30m

# Or run with all environment variables
sudo -E bash -c "
  export FLIGHTCTL_API_URL='${FLIGHTCTL_API_URL}'
  export FLIGHTCTL_TPM_CA_DIR='${FLIGHTCTL_TPM_CA_DIR}'
  go test -v -timeout 30m
"
```

### 4. Run with Specific Label

```bash
# Run only TPM hardware tests
sudo -E FLIGHTCTL_API_URL="${FLIGHTCTL_API_URL}" \
     go test -v -timeout 30m -ginkgo.label-filter="real-tpm"
```

## Test Execution Flow

The test will execute **15 steps** automatically:

1. ✅ **Hardware Prerequisites** - Verify TPM 2.0 device and tools
2. ✅ **TPM Manufacturer Detection** - Extract and identify EK certificate
3. ✅ **CA Certificate Configuration** - Setup manufacturer certificates
4. ✅ **Agent Installation** - Install from Copr repository
5. ✅ **Agent Configuration** - Enable TPM in agent config
6. ✅ **Service Startup** - Start flightctl-agent.service
7. ✅ **Enrollment Request** - Wait for TPM-based enrollment
8. ✅ **Attestation Verification** - Validate TPM attestation data
9. ✅ **Credential Challenge** - Verify challenge completion
10. ✅ **Enrollment Approval** - Approve enrollment via API
11. ✅ **Device Online Status** - Wait for device to come online
12. ✅ **Integrity Verification** - Verify "Verified" status (not "Failed")
13. ✅ **Key Persistence** - Check TPM blob file
14. ✅ **TPM Communication** - Verify TPM-signed device communication
15. ✅ **Summary Report** - Print comprehensive results

## Expected Test Duration

- **Normal execution**: 5-8 minutes
- **With slow network**: 10-15 minutes
- **Maximum timeout**: 30 minutes

## Expected Output

### Successful Test Run

```
Running Suite: TPM E2E Suite - /home/user/flightctl/test/e2e/tpm
═════════════════════════════════════════════════════════════════
GinkgoRandomSeed: 1698765432

Will run 1 of 1 specs
------------------------------

🔧 Using FlightCtl API: https://api.flightctl.example.com
✅ Test setup completed

• [STARTED] Real Hardware TPM Device Authentication Complete TPM Verification Workflow
  Should perform full TPM enrollment and verification on real hardware

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
      📄 Copied: stm_root_ca.pem -> /etc/flightctl/tpm-cas/stm_root_ca.pem
      📄 Copied: stm_intermediate_ca.pem -> /etc/flightctl/tpm-cas/stm_intermediate_ca.pem
    ✅ AIA extensions found in EK certificate
      📌 Intermediate CA URI: http://sw-center.st.com/STSAFE/stsafetpmrsaint10.crt
    ✅ Total CA certificates configured: 8

  Step 4: Installing FlightCtl agent from Copr repository
  📦 Installing FlightCtl agent from Copr repository...
    📌 Using Copr repository: https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev
    ✅ Copr repository enabled
    ✅ FlightCtl agent installed
    ✅ Agent binary: /usr/bin/flightctl-agent
    ℹ️  Agent version: 1.0.0~main~177~g873ca3fa-1

  Step 5: Configuring FlightCtl agent with TPM enabled
  ⚙️  Configuring FlightCtl agent with TPM enabled...
    ✅ Agent configuration written: /etc/flightctl/config.yaml
    📄 Configuration:
        server:
          url: https://api.flightctl.example.com
        tpm:
          enable: true
          device: /dev/tpm0
        enrollment:
          approve: false
        log:
          level: debug

  Step 6: Starting FlightCtl agent service
  🚀 Starting FlightCtl agent service...
    ✅ Service enabled
    ✅ Service started
    ✅ Service is active
    ✅ Agent using TPM-based identity

  Step 7: Waiting for TPM-based enrollment request
  ⏳ Waiting for enrollment request with TPM attestation...
    ✅ Enrollment request found: er-d4f7a9b2-3c8e-4f12-a5b9-7e8d3f4c9a1b

  Step 8: Verifying TPM attestation data in enrollment request
  🔍 Verifying TPM attestation data in enrollment request...
    📋 System Info: {AgentVersion:1.0 Architecture:x86_64 BootID:abc123 OperatingSystem:RHEL 9.4}
    ✅ TPM attestation data present
    📄 TPM Attestation Data:
      {
        "ek_cert": "MIIDrjCCA...",
        "lak_pub": "MIIBIjANBg...",
        "proof_of_possession": "..."
      }

  Step 9: Verifying credential challenge completion
  🔐 Verifying credential challenge completion...
    ✅ TPM verification label present
    ✅ Credential challenge verification completed

  Step 10: Approving enrollment request
  ✅ Approving enrollment request...
    ✅ Enrollment request approved
    ✅ Device created: device-rhel9-tpm-001

  Step 11: Waiting for device to come online
  ⏳ Waiting for device to come online...
    ✅ Device is online

  Step 12: Verifying TPM integrity checks passed
  🔐 Verifying TPM integrity checks...
    📋 TPM Integrity Status: Verified
    📋 Device Identity Status: Verified
    📋 Overall Integrity Status: Verified
    ✅ TPM integrity: Verified
    ✅ Device identity integrity: Verified
    ✅ Overall integrity: Verified

  Step 13: Verifying TPM key persistence
  🔑 Verifying TPM key persistence...
    ✅ TPM blob file exists: /var/lib/flightctl/tpm-blob.yaml
    📄 TPM Blob size: 2048 bytes
    ✅ TPM keys accessible

  Step 14: Verifying device communication using TPM identity
  💬 Verifying device communication using TPM identity...
    ✅ Agent using TPM identity
    ✅ Device communication active
    📋 Last seen: 12s ago
    ✅ TPM-based communication verified

  Step 15: Final verification - All TPM checks passed

  ═══════════════════════════════════════════════════════════════
  ✅ TPM VERIFICATION TEST PASSED - ALL CHECKS SUCCESSFUL
  ═══════════════════════════════════════════════════════════════

  📋 Test Summary:
    • Device ID: device-rhel9-tpm-001
    • Enrollment Request ID: er-d4f7a9b2-3c8e-4f12-a5b9-7e8d3f4c9a1b
    • TPM Manufacturer: STMicroelectronics

  ✅ Verified Components:
    • TPM 2.0 hardware detection
    • TPM manufacturer identification
    • TPM CA certificate chain configuration
    • FlightCtl agent installation from Copr
    • Agent TPM configuration
    • TPM key generation (LAK, LDevID)
    • TCG-CSR creation with attestation data
    • EK certificate chain validation
    • Credential challenge completion
    • Enrollment approval workflow
    • TPM integrity verification (Verified status)
    • Device identity verification (Verified status)
    • TPM key persistence
    • TPM-signed device communication

  🔐 Security Validation:
    • Hardware root of trust established
    • Certificate chain validated from device to manufacturer
    • Cryptographic proof of possession verified
    • Secure communication channel established

  ═══════════════════════════════════════════════════════════════

• [PASSED] in 180.45 seconds

✅ Test cleanup completed
------------------------------

Ran 1 of 1 Specs in 180.456 seconds
SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 0 Skipped
PASS
```

## Troubleshooting

### Issue: TPM device not found

```bash
# Check TPM presence
ls -la /dev/tpm*

# If not found, enable TPM in BIOS/UEFI
# Reboot system and enable TPM in firmware settings
```

### Issue: Permission denied accessing TPM

```bash
# Check permissions
ls -la /dev/tpm0

# Fix permissions if needed
sudo chmod 666 /dev/tpm0

# Or add user to tss group
sudo usermod -aG tss $(whoami)
newgrp tss
```

### Issue: Agent installation fails

```bash
# Enable Copr repo manually
sudo dnf copr enable -y @redhat-et/flightctl-dev

# List available packages
sudo dnf list available flightctl\*

# Install manually
sudo dnf install -y flightctl

# Verify installation
which flightctl-agent
flightctl-agent --version
```

### Issue: Certificate chain validation fails

```bash
# Check API server logs
kubectl logs -n flightctl deployment/flightctl-api | grep -i tpm

# Verify CA certificates are mounted
kubectl exec -n flightctl deployment/flightctl-api -- ls -la /etc/flightctl/tpm-cas/

# Check if correct manufacturer certificates are present
kubectl exec -n flightctl deployment/flightctl-api -- cat /etc/flightctl/config.yaml | grep -A 10 tpmCAPaths
```

### Issue: Integrity status shows "Failed" instead of "Verified"

This indicates certificate chain validation failed. Common causes:

1. **API server missing TPM CA certificates**
   ```bash
   # Verify certificates on API server
   kubectl get configmap tpm-ca-certs -n flightctl
   ```

2. **Wrong manufacturer certificates configured**
   ```bash
   # Check your TPM manufacturer
   sudo tpm2_nvread 0x01c00002 -o /tmp/ek.der
   openssl x509 -inform DER -in /tmp/ek.der -text -noout | grep Issuer
   
   # Ensure matching manufacturer CAs are configured
   ```

3. **Certificate chain incomplete**
   ```bash
   # Verify full chain is present (root + intermediate)
   ls -la /etc/flightctl/tpm-cas/
   ```

### Issue: Test hangs at credential challenge

```bash
# Check gRPC connectivity
curl -I -k https://api.flightctl.example.com

# Check agent logs for errors
sudo journalctl -u flightctl-agent -f

# Verify firewall allows gRPC (HTTP/2)
sudo firewall-cmd --list-all
```

## Viewing Agent Logs

```bash
# Real-time logs
sudo journalctl -u flightctl-agent -f

# Filter for TPM
sudo journalctl -u flightctl-agent | grep -i tpm

# Filter for errors
sudo journalctl -u flightctl-agent | grep -i error

# Last 100 lines
sudo journalctl -u flightctl-agent -n 100 --no-pager
```

## Cleanup After Test

The test automatically cleans up resources, but if you need manual cleanup:

```bash
# Stop agent service
sudo systemctl stop flightctl-agent
sudo systemctl disable flightctl-agent

# Remove agent data (keeps certs)
sudo rm -rf /var/lib/flightctl/db
sudo rm -f /var/lib/flightctl/tpm-blob.yaml

# Uninstall agent package (optional)
sudo dnf remove -y flightctl

# Disable Copr repo (optional)
sudo dnf copr disable @redhat-et/flightctl-dev
```

## Additional Resources

- [TPM Authentication Documentation](../../../docs/user/tpm-authentication.md)
- [Agent Configuration Guide](../../../docs/user/configuring-agent.md)
- [FlightCtl Copr Repository](https://copr.fedorainfracloud.org/coprs/g/redhat-et/flightctl-dev/packages/)
- [TCG Device Identity Specification](https://trustedcomputinggroup.org/wp-content/uploads/TPM-2.0-Keys-for-Device-Identity-and-Attestation-v1.10r9_pub.pdf)
- [Google Doc - TPM Certificates](https://docs.google.com/document/d/1ajtuiKfydg93iLcTLPSQdCTQQQ7wvMh-dTB5i17Loag/edit?usp=sharing)

## Support

If you encounter issues not covered in this guide:

1. Check agent logs: `sudo journalctl -u flightctl-agent -n 200`
2. Check API server logs: `kubectl logs -n flightctl deployment/flightctl-api`
3. Verify TPM hardware: `sudo tpm2_getcap properties-fixed`
4. Review test output for specific failure point
5. Open an issue at https://github.com/flightctl/flightctl/issues


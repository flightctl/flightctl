# TPM Device Authentication

This document describes how Flight Control uses Trusted Platform Module (TPM) 2.0 for hardware-based device identity and authentication.

## Table of Contents

- [Overview](#overview)
- [TPM Authentication Architecture](#tpm-authentication-architecture)
- [Certificate Requirements](#certificate-requirements)
- [Enabling TPM in Agent Configuration](#enabling-tpm-in-agent-configuration)
- [Enrollment Process](#enrollment-process)
- [Troubleshooting](#troubleshooting)

## Overview

Flight Control supports TPM 2.0 hardware modules for establishing device identity and secure authentication. TPM provides a hardware root-of-trust that ensures cryptographic keys are protected by dedicated security hardware and cannot be extracted or cloned.

### Key Features

- **Hardware-Protected Keys**: Private keys generated and stored within the TPM
- **Device Attestation**: Cryptographic proof of device identity and integrity
- **Certificate Chain Validation**: Full chain verification from device to TPM manufacturer
- **TCG Compliant CSR**: Certificate requests follow Trusted Computing Group specifications

## TPM Authentication Architecture

Flight Control's TPM implementation follows the TCG (Trusted Computing Group) specifications for device identity:

### Key Hierarchy

The agent creates and manages three key types within the TPM:

1. **Endorsement Key (EK)**: Pre-installed by TPM manufacturer, provides root identity
2. **Local Attestation Key (LAK)**: Generated locally for attestation operations
3. **Local Device Identity (LDevID)**: Generated locally for device authentication

### Certificate Chain

TPM authentication requires a complete certificate chain from the TPM manufacturer:

```text
TPM Manufacturer Root CA
    └── TPM Manufacturer Intermediate CA
            └── Endorsement Key (EK) Certificate
                    └── Device Identity
```

## Certificate Requirements

### TPM CA Certificates

For TPM authentication to work properly, you must provide:

1. **Root CA Certificate**: The root certificate authority from the TPM manufacturer
2. **Intermediate CA Certificate(s)**: Any intermediate CAs in the chain

### Installing TPM CA Certificates

The Flight Control service needs access to the TPM manufacturer's CA certificates to validate device enrollment requests. These certificates must be added to the service's trust store.

## Configuring TPM CA Certificates in Flight Control Service

The Flight Control API server needs TPM manufacturer CA certificates to validate device enrollment requests.

### Obtaining TPM CA Certificates

If direct access to the device is possible, required certs can be discovered by first obtaining the Endorsement Key cert
and then following the Authority Information Access (AIA) chain. Well known TPM NVRAM index handles are [defined by the TCG](https://trustedcomputinggroup.org/wp-content/uploads/TCG_IWG_EKCredentialProfile_v2p4_r3.pdf).

#### Example: Starting from a device

Note: access to the TPM typically requires root privileges.
Note: [TPM tools](https://tpm2-tools.readthedocs.io/en/latest/INSTALL/) must be installed. These are available via `dnf` also.

```bash
sudo tpm2_nvread 0x01c00002 -o rsa_ek_cert.der
sudo openssl x509 -inform DER -in rsa_ek_cert.der -text -noout 
```

`0x01c00002` is the well-known address of the RSA EK Cert.

Expected output:

```bash
...elided
       X509v3 extensions:
            ...elided
            Authority Information Access: 
                CA Issuers - URI:http://sw-center.st.com/STSAFE/stsafetpmrsaint10.crt
```

The intermediate cert can be downloaded as described in the following example, and used to find the root cert in the AIA chain.

#### Example: Infineon TPM CA Certificates

```bash
# Download Infineon TPM Root CA
curl -O https://www.infineon.com/dgdl/Infineon-TPM_RSA_Root_CA-C-v01_00-EN.cer

# Download Infineon TPM Intermediate CA  
curl -O https://www.infineon.com/dgdl/Infineon-TPM_RSA_Intermediate_CA_01-C-v01_00-EN.cer

# Convert to PEM format if needed
openssl x509 -inform DER -in Infineon-TPM_RSA_Root_CA-C-v01_00-EN.cer \
    -out infineon-root-ca.pem

openssl x509 -inform DER -in Infineon-TPM_RSA_Intermediate_CA_01-C-v01_00-EN.cer \
    -out infineon-intermediate-ca.pem
```

### Kubernetes Deployment

For Kubernetes deployments, add TPM CA certificates via ConfigMap and configure the API server:

1. **Create ConfigMap with TPM CA certificates:**

```bash
kubectl create configmap tpm-ca-certs \
  --from-file=infineon-root-ca.pem \
  --from-file=infineon-intermediate-ca.pem \
  --namespace=flightctl
```

1. **Update the API server configuration to include TPM CA paths:**

```yaml
# Get current API configuration
kubectl get configmap flightctl-api-config -n flightctl -o yaml

# Add tpmCAPaths to the service section
apiVersion: v1
kind: ConfigMap
metadata:
  name: flightctl-api-config
  namespace: flightctl
data:
  config.yaml: |
    service:
      tpmCAPaths:
        - /etc/flightctl/tpm-cas/infineon-root-ca.pem
        - /etc/flightctl/tpm-cas/infineon-intermediate-ca.pem
      # ... rest of service configuration
```

1. **Mount the TPM CA certificates in the API server deployment:**

```bash
kubectl patch deployment flightctl-api -n flightctl --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/volumes/-",
    "value": {
      "name": "tpm-ca-certs",
      "configMap": {
        "name": "tpm-ca-certs"
      }
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
```

1. **Wait for the deployment to roll out:**

```bash
kubectl rollout status deployment/flightctl-api -n flightctl
```

### Standalone Deployment

For standalone installations:

```bash
# Create TPM CA certificate directory
sudo mkdir -p /etc/flightctl/tpm-cas

# Copy TPM manufacturer CA certificates
sudo cp infineon-root-ca.pem /etc/flightctl/tpm-cas/
sudo cp infineon-intermediate-ca.pem /etc/flightctl/tpm-cas/

# Set appropriate permissions
sudo chmod 644 /etc/flightctl/tpm-cas/*.pem
```

Configure the API server:

```yaml
# /etc/flightctl/server.yaml or config.yaml
service:
  tpmCAPaths:
    - /etc/flightctl/tpm-cas/infineon-root-ca.pem
    - /etc/flightctl/tpm-cas/infineon-intermediate-ca.pem
  # ... rest of service configuration
```

> [!NOTE]
> Different TPM manufacturers provide their CA certificates in various locations:
>
> - **Infineon**: <https://www.infineon.com/cms/en/product/security-smart-card-solutions/optiga-embedded-security-solutions/>
> - **STMicroelectronics**: See [Technical Note TN1330](https://www.st.com/resource/en/technical_note/tn1330-st-trusted-platform-module-tpm-endorsement-key-ek-certificates-stmicroelectronics.pdf) for certificates
> - **Nuvoton**: Available through Nuvoton support portal

## Enabling TPM in Agent Configuration

To enable TPM authentication, configure the `tpm` section in the agent's `/etc/flightctl/config.yaml` file. See [Configuring the Flight Control Agent - TPM Configuration](configuring-agent.md#tpm-configuration) for detailed configuration parameters and examples.

## Enrollment Process

When TPM is enabled, the device enrollment process follows these steps:

### 1. Key Generation

On first boot, the agent:

- Generates Local Attestation Key (LAK) in TPM
- Generates Local Device Identity Key (LDevID) in TPM
- Retrieves Endorsement Key certificate from TPM

### 2. TCG-CSR Generation

The agent creates a TCG-compliant Certificate Signing Request containing:

- Standard X.509 CSR fields
- TPM attestation data
- EK certificate
- Hardware platform information (model, serial number)
- Cryptographic proof of key possession

### 3. Server Validation

The Flight Control service:

- Validates the CSR signature
- Verifies TPM attestation data
- Validates EK certificate chain against TPM CA certificates
- Checks device approval status

### 4. Certificate Issuance

Upon successful validation and approval:

- Service issues management certificate
- Certificate is bound to TPM-protected key
- Device uses certificate for all subsequent operations

## Troubleshooting

For agent-specific TPM troubleshooting and debugging, see [Configuring the Flight Control Agent - TPM Troubleshooting](configuring-agent.md#tpm-troubleshooting).

## Security Considerations

### Hardware Requirements

- TPM 2.0 compliant module (TPM 1.2 is not supported)
- Secure Boot recommended for full chain of trust
- UEFI firmware with TPM support

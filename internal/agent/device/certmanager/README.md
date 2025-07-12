# FlightCtl Agent Certificate Management System

The FlightCtl Agent Certificate Management System provides automated certificate provisioning, validation, and renewal for devices managed by FlightCtl.

## Overview

This system allows agents to:
- **Automatically provision certificates** using configurable provisioners
- **Validate certificate status** and expiration dates
- **Renew certificates** before they expire
- **Handle configuration changes** by adding/removing certificates dynamically
- **Store certificates** using pluggable storage providers

## Architecture

The certificate management system consists of several key components:

### Core Components

1. **CertManager** - Main orchestrator that manages the certificate lifecycle
2. **Storage Providers** - Handle certificate and key storage (filesystem, keystore, etc.)
3. **Provisioners** - Handle certificate creation (CSR, external, etc.)
4. **Configuration** - YAML-based configuration for certificates

### Interfaces

```go
// Manager interface for certificate management
type Manager interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Sync(ctx context.Context) error
    GetCertificateStatus(ctx context.Context) (map[string]CertificateStatus, error)
    RenewCertificate(ctx context.Context, certName string) error
    ValidateCertificate(ctx context.Context, certName string) (*CertificateInfo, error)
}

// Storage interface for certificate storage
type Storage interface {
    Load(ctx context.Context) (certPEM, keyPEM []byte, err error)
    Writer(ctx context.Context) (StorageWriter, error)
    Delete(ctx context.Context) error
}

// Provisioner interface for certificate provisioning
type Provisioner interface {
    Provision(ctx context.Context) error
    Result(ctx context.Context, w StorageWriter) error
}
```

## Configuration

Certificates are configured in the agent configuration file under the `certificates` section:

```yaml
certificates:
  - name: agent                          # Unique certificate name
    provisioner:                         # Provisioner configuration
      csr:                              # CSR provisioner
        signer: flightctl.io/agent      # CSR signer name
        common-name: agent              # Certificate common name
        key-usage:                      # Key usage extensions
          - digitalSignature
          - keyEncipherment
        extended-key-usage:             # Extended key usage
          - serverAuth
          - clientAuth
    storage:                            # Storage configuration
      filesystem:                       # Filesystem storage
        cert-path: /var/lib/flightctl/certs/agent.crt
        key-path: /var/lib/flightctl/certs/agent.key
        permissions: 0600
    renewal-threshold: 168h             # Renewal threshold (7 days)
```

### Provisioner Types

#### CSR Provisioner (`csr:`)
Creates certificates using Certificate Signing Requests submitted to the FlightCtl management API.

**Configuration:**
```yaml
provisioner:
  csr:
    signer: flightctl.io/agent          # Required: CSR signer name
    common-name: agent                  # Optional: Certificate common name
    key-usage:                          # Optional: Key usage extensions
      - digitalSignature
      - keyEncipherment
    extended-key-usage:                 # Optional: Extended key usage
      - serverAuth
      - clientAuth
    config:                            # Optional: Additional configuration
      subject-alt-names:               # Subject Alternative Names
        - DNS:agent.flightctl.io
        - IP:192.168.1.100
```

#### External Provisioner (`external:`)
*Coming soon* - Integrates with external certificate authorities.

**Configuration:**
```yaml
provisioner:
  external:
    url: https://ca.example.com/api/v1/certificates
    config:
      ca-name: enterprise-ca
      template: device-template
```

### Storage Types

#### Filesystem Storage (`filesystem:`)
Stores certificates and keys as files on the local filesystem using base64 encoding.

**Configuration:**
```yaml
storage:
  filesystem:
    cert-path: /var/lib/flightctl/certs/agent.crt  # Certificate file path
    key-path: /var/lib/flightctl/certs/agent.key   # Private key file path
    permissions: 0600                              # File permissions
```

#### TPM Storage (`tpm:`)
*Coming soon* - Stores certificates and keys in TPM hardware.

**Configuration:**
```yaml
storage:
  tpm:
    tpm-path: /dev/tpm0                # TPM device path
    key-handle: 0x81000001             # TPM key handle
    config:
      auth-policy: "password"          # Authentication policy
      hierarchy: "owner"               # TPM hierarchy
```

#### Keystore Storage (`keystore:`)
*Coming soon* - Stores certificates in system keystores.

**Configuration:**
```yaml
storage:
  keystore:
    keystore-path: /var/lib/flightctl/certs/device.p12  # Keystore file path
    keystore-type: PKCS12                               # Keystore type
    password: changeme                                  # Keystore password
    config:
      alias: device-cert                                # Certificate alias
```

## Certificate Lifecycle

### 1. Initialization
When the certificate manager starts, it:
- Loads certificate configurations from the agent config
- Creates storage and provisioner instances for each certificate
- Validates existing certificates

### 2. Validation
For each certificate, the manager:
- Checks if the certificate and key files exist
- Parses the certificate to extract metadata
- Validates the certificate is not expired
- Determines if renewal is needed based on the renewal threshold

### 3. Provisioning
When a certificate doesn't exist, the manager:
- Uses the configured provisioner to create a new certificate
- Stores the certificate and key using the configured storage
- Updates the certificate status

### 4. Renewal
When a certificate needs renewal, the manager:
- Deletes the old certificate
- Provisions a new certificate using the same configuration
- Stores the new certificate and key

### 5. Configuration Changes
The manager continuously monitors for configuration changes:
- **New certificates** are added and provisioned
- **Removed certificates** are deleted from storage
- **Modified certificates** are updated according to the new configuration

## Certificate Status

The system tracks detailed status information for each certificate:

```go
type CertificateStatus struct {
    Name         string    `json:"name"`          // Certificate name
    Exists       bool      `json:"exists"`        // Whether cert exists
    ExpiresAt    time.Time `json:"expires_at"`    // Expiration time
    ValidFrom    time.Time `json:"valid_from"`    // Valid from time
    CommonName   string    `json:"common_name"`   // Certificate CN
    IsExpired    bool      `json:"is_expired"`    // Is expired
    NeedsRenewal bool      `json:"needs_renewal"` // Needs renewal
    Error        string    `json:"error"`         // Error message
}
```

## Usage Examples

### Basic Agent Certificate
```yaml
certificates:
  - name: agent
    provisioner:
      csr:
        signer: flightctl.io/agent
        common-name: agent
        key-usage:
          - digitalSignature
          - keyEncipherment
        extended-key-usage:
          - serverAuth
          - clientAuth
    storage:
      filesystem:
        cert-path: /var/lib/flightctl/certs/agent.crt
        key-path: /var/lib/flightctl/certs/agent.key
        permissions: 0600
    renewal-threshold: 168h # 7 days
```

### Multiple Certificates with Different Storage Types
```yaml
certificates:
  # Main agent certificate using filesystem storage
  - name: agent
    provisioner:
      csr:
        signer: flightctl.io/agent
        common-name: agent
        key-usage:
          - digitalSignature
          - keyEncipherment
        extended-key-usage:
          - serverAuth
          - clientAuth
    storage:
      filesystem:
        cert-path: /var/lib/flightctl/certs/agent.crt
        key-path: /var/lib/flightctl/certs/agent.key
        permissions: 0600
    renewal-threshold: 168h

  # OpenTelemetry certificate for metrics
  - name: otel
    provisioner:
      csr:
        signer: flightctl.io/device-svc-client
        common-name: otel-collector
        key-usage:
          - digitalSignature
          - keyEncipherment
        extended-key-usage:
          - clientAuth
    storage:
      filesystem:
        cert-path: /etc/otel/cert.pem
        key-path: /etc/otel/key.pem
        permissions: 0600
    renewal-threshold: 72h # 3 days

  # Example TPM certificate (future implementation)
  # - name: tpm-cert
  #   provisioner:
  #     csr:
  #       signer: flightctl.io/tpm-device
  #       common-name: tpm-device
  #   storage:
  #     tpm:
  #       tpm-path: /dev/tpm0
  #       key-handle: 0x81000001
  #   renewal-threshold: 168h
```

## API Usage

### Programmatic Certificate Management

```go
// Create certificate manager
certManager := cert.NewManager(deviceName, config, readWriter, logger)
certManager.SetManagementClient(managementClient)

// Start certificate management
ctx := context.Background()
if err := certManager.Start(ctx); err != nil {
    log.Fatalf("Failed to start certificate manager: %v", err)
}

// Get certificate status
status, err := certManager.GetCertificateStatus(ctx)
if err != nil {
    log.Errorf("Failed to get certificate status: %v", err)
}

// Force certificate renewal
if err := certManager.RenewCertificate(ctx, "agent"); err != nil {
    log.Errorf("Failed to renew certificate: %v", err)
}

// Validate specific certificate
info, err := certManager.ValidateCertificate(ctx, "agent")
if err != nil {
    log.Errorf("Failed to validate certificate: %v", err)
}
```

## Monitoring and Observability

The certificate manager provides extensive logging and status reporting:

- **Certificate lifecycle events** (creation, renewal, deletion)
- **Validation results** (expiration, errors)
- **Configuration changes** (additions, removals)
- **Provisioning status** (success, failures)

### Log Examples

```
INFO  Starting certificate manager
INFO  Added certificate for management: agent
INFO  Certificate agent does not exist, creating...
INFO  Creating certificate: agent
INFO  Successfully created certificate: agent
INFO  Certificate agent needs renewal, renewing...
INFO  Renewing certificate: agent
WARN  Failed to delete old certificate agent: file not found
INFO  Successfully created certificate: agent
```

## Security Considerations

1. **Private Key Protection**: Private keys are stored with restrictive permissions (0600)
2. **Certificate Validation**: All certificates are validated before use
3. **Secure Storage**: Certificates are base64 encoded and stored securely
4. **Renewal Thresholds**: Configurable renewal thresholds prevent expired certificates
5. **Error Handling**: Comprehensive error handling prevents security vulnerabilities

## Troubleshooting

### Common Issues

1. **Certificate Not Found**
   - Check that the certificate paths are correct
   - Verify the storage configuration
   - Ensure the provisioner has completed successfully

2. **Certificate Renewal Failures**
   - Check the management client connectivity
   - Verify the CSR signer is available
   - Review the certificate configuration

3. **Permission Errors**
   - Ensure the agent has write permissions to certificate directories
   - Check file permissions on existing certificates

### Debug Logging

Enable debug logging to see detailed certificate management operations:

```yaml
log-level: debug
```

## Future Enhancements

- **TPM Integration**: Hardware-backed certificate storage
- **External CAs**: Integration with external certificate authorities
- **Certificate Templates**: Configurable certificate templates
- **Metrics**: Prometheus metrics for certificate lifecycle
- **Webhooks**: Webhook notifications for certificate events 
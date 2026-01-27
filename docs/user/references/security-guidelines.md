# Flight Control Security Guidelines

This document provides comprehensive security guidelines for deploying and operating Flight Control in production environments. It covers authentication mechanisms, data protection, insecure settings, and security best practices.

## Table of Contents

- [Overview](#overview)
- [Authentication and Authorization](#authentication-and-authorization)
- [Data Protection](#data-protection)
- [Network Security](#network-security)
  - [Rate Limiting](#rate-limiting)
- [Device Security](#device-security)
- [Database Security](#database-security)
- [Logging and Auditing](#logging-and-auditing)
- [Insecure Settings and Default Configurations](#insecure-settings-and-default-configurations)

## Overview

Flight Control is a device management platform that provides secure enrollment, configuration management, and monitoring of edge devices. The system uses hardware root-of-trust, mutual TLS authentication, and certificate-based identity management to ensure secure device operations.

### Security Architecture

Flight Control implements a multi-layered security approach:

- **Hardware Root-of-Trust**: TPM-based device identity and attestation
- **Certificate-Based Authentication**: X.509 certificates for all communications
- **Mutual TLS (mTLS)**: Bidirectional certificate verification
- **Role-Based Access Control**: Granular permissions for users and devices
- **Encrypted Communications**: TLS 1.3 for all network communications

## Authentication and Authorization

### User Authentication

Flight Control supports multiple authentication providers:

#### Kubernetes Authentication

- **Type**: `k8s`
- **Description**: Uses Kubernetes service account tokens for authentication

```yaml
auth:
  type: k8s
  k8s:
    serviceAccountToken: "/var/run/secrets/kubernetes.io/serviceaccount/token"
```

#### OIDC Authentication

- **Type**: `oidc`
- **Description**: OpenID Connect authentication with external providers

```yaml
auth:
  type: oidc
  oidc:
    issuerURL: "https://your-oidc-provider.com"
    clientID: "flightctl-client"
    clientSecret: "your-client-secret"
```

#### AAP (Ansible Automation Platform) Authentication

- **Type**: `aap`
- **Description**: Integration with Ansible Automation Platform

```yaml
auth:
  type: aap
  aap:
    serverURL: "https://your-aap-server.com"
    username: "your-username"
    password: "your-password"
```

### Device Authentication

Devices authenticate using X.509 client certificates:

#### Enrollment Process

1. **Hardware Identity**: Device generates cryptographic key pair using TPM
2. **Certificate Signing Request**: Device submits CSR with hardware fingerprint
3. **Manual Approval**: Administrator approves enrollment request
4. **Certificate Issuance**: Service issues device-specific management certificate
5. **Secure Communication**: Device uses management certificate for all subsequent communications

#### Certificate Management

- **Enrollment Certificates**: Used only for initial enrollment (configurable validity).
- **Management Certificates**: Device-specific certificates used by the agent for ongoing
  communication with the Flight Control service.
- **Hardware Protection**: Private keys are protected by TPM when available.
- **Automatic Rotation**: The device **management certificate** is automatically renewed
  by the agent before expiration. Rotation is handled internally by the agent
  and does not require re-enrollment or administrator action.

### Authorization

#### User Permissions

Flight Control implements role-based access control:

- **Device Management**: Create, read, update, delete devices
- **Fleet Management**: Manage fleets and templates
- **Enrollment Approval**: Approve or deny device enrollment requests
- **System Administration**: Access to system configuration and monitoring

#### Device Permissions

- **Self-Management**: Devices can only modify their own configuration
- **Status Reporting**: Devices can report their status and health
- **Template Application**: Devices can apply approved configuration templates

## Data Protection

### Data in Transit

All communications are encrypted using TLS 1.3:

#### API Communications

- **User API**: HTTPS with TLS 1.3, minimum version enforced
- **Agent API**: Mutual TLS with client certificate verification
- **Database Connections**: TLS encryption for PostgreSQL connections
- **Redis Communications**: TLS encryption for key-value store

#### Certificate Configuration

```yaml
# Server certificate configuration
service:
  srvCertFile: "/path/to/server.crt"
  srvKeyFile: "/path/to/server.key"
  altNames: ["flightctl.example.com", "localhost"]

# Client certificate configuration
auth:
  clientCertificate: "/path/to/client.crt"
  clientKey: "/path/to/client.key"
```

### Data at Rest

#### Database Encryption

- **PostgreSQL**: Supports full database encryption, but this is *NOT* enabled by default
- **Connection Encryption**: TLS for all database connections
- **Credential Storage**: Database credentials are stored in plain text or base64 encoding (*NOT* encrypted) - in Kubernetes Secrets when deployed on k8s, and in Podman secrets when deployed with quadlets

#### File System Security

- **Certificate Storage**: Certificates stored with 0600 permissions
- **Configuration Files**: Configuration files stored with 0600 permissions
- **Log Files**: Log files stored with appropriate permissions

### Data Privacy

#### Data Collection

Flight Control collects the following data:

- **Device Information**: Hardware specifications, operating system details
- **System Status**: Health metrics, application status, resource usage
- **Configuration**: Applied configuration templates and settings
- **Events**: User actions, system events, security events

#### Data Retention

- **Event Logs**: Configurable retention period (default: 7 days)
- **Device Status**: Retained for device lifecycle

#### Data Access Controls

- **Organization Isolation**: Data is isolated by organization
- **Role-Based Access**: Users can only access authorized data

## Network Security

Flight Control requires specific network ports and firewall configuration. For detailed network requirements, see [Preparing Installation on Linux](../installing/preparing-installation-on-linux.md).

### Rate Limiting

Flight Control implements IP-based rate limiting to protect against abuse and ensure fair usage. Proper configuration is essential for security, especially when using reverse proxies.

#### Rate Limiting Security

- **IP-Based Protection**: Rate limiting is applied per client IP address
- **Proxy Header Support**: Automatically handles X-Forwarded-For, X-Real-IP, and True-Client-IP headers
- **Separate Auth Limits**: Stricter rate limits for authentication endpoints
- **Configurable Limits**: Adjustable limits for different environments

#### Security Considerations

1. **Reverse Proxy Configuration**: When using reverse proxies, ensure they're configured to send real client IPs
2. **Trusted Proxies**: Only allow trusted reverse proxies to set IP headers
3. **Network Isolation**: Restrict direct access to the Flight Control API
4. **Monitor Abuse**: Watch for unusual rate limiting patterns

For detailed rate limiting configuration and reverse proxy setup, see [Rate Limiting](../installing/configuring-rate-limiting.md).

### Service Endpoints

- **User API**: Port 3443 (HTTPS)
- **Agent API**: Port 7443 (mTLS)
- **Database**: Port 5432 (TLS)
- **Redis**: Port 6379 (TLS)
- **Monitoring**: Port 15690 (Prometheus metrics)

### Network Security Features

#### TLS Configuration

- **Minimum Version**: TLS 1.3
- **Cipher Suites**: Strong cipher suites only
- **Certificate Verification**: Strict certificate validation
- **SNI Support**: Server Name Indication for proper certificate selection

#### Network Isolation

- **Internal Services**: Services communicate over internal network
- **External Access**: Limited external access to required endpoints only

## Device Security

### Device Security Features

#### Hardware Security

- **TPM Integration**: Hardware root-of-trust using TPM 2.0
- **Secure Boot**: Compatible with secure boot implementations
- **Hardware Binding**: Device identity bound to specific hardware
- **SELinux Support**: Agent supports SELinux for mandatory access control
- **FIPS Compliance**: Agent is FIPS 140-2 compliant for cryptographic operations

### Device Authentication

Devices authenticate using X.509 client certificates:

#### Enrollment Process

1. **Hardware Identity**: Device generates cryptographic key pair using TPM
2. **Certificate Signing Request**: Device submits CSR with hardware fingerprint
3. **Manual Approval**: Administrator approves enrollment request
4. **Certificate Issuance**: Service issues device-specific management certificate
5. **Secure Communication**: Device uses management certificate for all subsequent communications

#### Certificate Management

- **Enrollment Certificates**: Used only for initial enrollment (configurable validity).
- **Management Certificates**: Device-specific certificates used by the agent for ongoing
  communication with the Flight Control service.
- **Hardware Protection**: Private keys are protected by TPM when available.
- **Automatic Rotation**: The device **management certificate** is automatically renewed
  by the agent before expiration. Rotation is handled internally by the agent
  and does not require re-enrollment or administrator action.

### Authorization

#### User Permissions

Flight Control implements role-based access control:

- **Device Management**: Create, read, update, delete devices
- **Fleet Management**: Manage fleets and templates
- **Enrollment Approval**: Approve or deny device enrollment requests
- **System Administration**: Access to system configuration and monitoring

#### Device Permissions

- **Self-Management**: Devices can only modify their own configuration
- **Status Reporting**: Devices can report their status and health
- **Template Application**: Devices can apply approved configuration templates

## Database Security

### Database Access Control

Flight Control implements a three-user database model for security. For detailed database configuration and migration information, see [Database Migration](../installing/performing-database-migration.md).

#### User Roles

1. **Admin User**: Full database administration privileges
2. **Migration User**: Schema changes and migrations only
3. **Application User**: Runtime data operations only

### Database Security Features

#### Connection Security

- **TLS Encryption**: All database connections use TLS
- **Certificate Authentication**: Optional client certificate authentication
- **Connection Pooling**: Secure connection pooling with proper cleanup

## Logging and Auditing

### Logging

Flight Control provides structured logging for operational monitoring:

#### Logged Events

- **Service Events**: Service startup, shutdown, configuration changes
- **Device Events**: Enrollment, configuration changes, decommissioning
- **Error Events**: Application errors and failures
- **Performance Events**: System performance metrics

#### Log Format

```json
{
  "timestamp": "2024-01-15T10:30:00Z",
  "level": "info",
  "component": "service",
  "message": "Service started successfully"
}
```

### Log Security

#### Log Protection

- **File Permissions**: Log files stored with appropriate permissions
- **Log Rotation**: Handled by deployment platform (Kubernetes or systemd/journald)

## Data Privacy

### Personal Data Handling

#### Data Minimization

- **Required Data Only**: Collect only necessary data
- **Data Retention**: Limited data retention periods
- **Data Deletion**: Automatic deletion of expired data

#### Privacy Protection

- **Access Controls**: Strict access controls on personal data

## Insecure Settings and Default Configurations

### Default Security Settings

#### Insecure Defaults

Flight Control has several insecure default settings that must be changed in production:

```yaml
# INSECURE DEFAULTS - CHANGE IN PRODUCTION
database:
  password: "adminpass"  # Change to strong password
  migrationPassword: "adminpass"  # Change to strong password

kv:
  password: "adminpass"  # Change to strong password

service:
  # Self-signed certificates by default
  srvCertFile: ""  # Use proper certificates in production
  srvKeyFile: ""   # Use proper certificates in production
```

#### Required Security Changes

1. **Database Passwords**: Change all default database passwords
2. **Redis Password**: Change default Redis password
3. **TLS Certificates**: Replace self-signed certificates with proper certificates
4. **Authentication**: Configure proper authentication provider
5. **Network Access**: Restrict network access to required ports only
6. **Configure Firewall**: Restrict network access
7. **Enable Logging**: Configure logging

### Development vs Production

#### Development Settings

```yaml
# Development settings (INSECURE)
auth:
  insecureSkipTlsVerify: true  # Skip TLS verification

service:
  # Self-signed certificates
  srvCertFile: ""
  srvKeyFile: ""
```

#### Production Settings

```yaml
# Production settings (SECURE)
auth:
  insecureSkipTlsVerify: false  # Always verify TLS

service:
  # Proper certificates
  srvCertFile: "/etc/flightctl/certs/server.crt"
  srvKeyFile: "/etc/flightctl/certs/server.key"
```

## Conclusion

Flight Control provides a secure foundation for device management with comprehensive security features. However, proper configuration and ongoing security management are essential for maintaining security in production environments.

### Key Security Recommendations

1. **Change Default Passwords**: Immediately change all default passwords
2. **Use Proper Certificates**: Replace self-signed certificates with proper certificates
3. **Configure Authentication**: Set up proper authentication provider
4. **Regular Updates**: Keep all components updated
5. **Security Training**: Provide security training for staff
6. **Regular Audits**: Conduct regular security audits
7. **Incident Response**: Have incident response procedures

---

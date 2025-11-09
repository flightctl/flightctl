# PAM Authentication

This document describes how to use PAM authentication with Flight Control.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [User Management](#user-management)
  - [Adding Users to PAM Issuer](#adding-users-to-pam-issuer)
  - [Using Host System Users (Advanced)](#using-host-system-users-advanced)
- [Security Considerations](#security-considerations)

## Overview

The PAM issuer is an OpenID Connect (OIDC) implementation that Flight Control provides out of the box for standalone and Quadlet deployments. It enables user authentication using Linux PAM (Pluggable Authentication Modules), allowing administrators to authenticate users with standard Linux user credentials.

The PAM issuer runs as a separate service (`flightctl-pam-issuer`) and provides OIDC-compliant authentication endpoints that integrate with the main Flight Control API server.

## Prerequisites

There are no special prerequisites for using PAM authentication. The PAM issuer service is included in Quadlet and standalone Flight Control deployments.

## Configuration

The PAM issuer is configured in the Flight Control configuration file (typically `~/.flightctl/config.yaml` or `/etc/flightctl/config.yaml`). The configuration is located under the `auth.pamOidcIssuer` section.

### Configuration Options

```yaml
auth:
  pamOidcIssuer:
    address: ":8444"                                    # Listen address for the PAM issuer service
    issuer: "https://localhost:8444/api/v1/auth"       # Base URL for the OIDC issuer
    clientId: "flightctl-client"                       # OAuth2 client ID
    clientSecret: ""                                    # OAuth2 client secret (empty for public clients)
    scopes:                                             # Supported OAuth2 scopes
      - "openid"
      - "profile"
      - "email"
      - "roles"
    redirectUris:                                       # Allowed redirect URIs for OAuth2 flows
      - "http://localhost:7777/callback"
    pamService: "flightctl"                            # PAM service name (default: "flightctl")
    allowPublicClientWithoutPKCE: false                # SECURITY: Allow public clients without PKCE (not recommended)
```

### Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `address` | Listen address for the PAM issuer service | `:8444` |
| `issuer` | Base URL for the OIDC issuer. Must match where `.well-known/openid-configuration` is served | Service base URL |
| `clientId` | OAuth2 client ID for authentication | `flightctl-client` |
| `clientSecret` | OAuth2 client secret. Leave empty for public clients (CLI) | Empty |
| `scopes` | OAuth2 scopes supported by the issuer | `["openid", "profile", "email", "roles"]` |
| `redirectUris` | Allowed redirect URIs for OAuth2 authorization code flow | Based on service base URL |
| `pamService` | PAM service name to use for authentication (must match `/etc/pam.d/<name>`) | `flightctl` |
| `allowPublicClientWithoutPKCE` | Allow public clients to skip PKCE requirement. **Security Warning**: Only enable for testing | `false` |

### Default Configuration

If the `pamOidcIssuer` section is present in the configuration file, the following defaults are automatically applied:

- **PAM Service**: Defaults to `"flightctl"`
- **Issuer URL**: Defaults to the service's base URL if not specified
- **Client ID**: Defaults to `"flightctl-client"`
- **Scopes**: Defaults to `["openid", "profile", "email", "roles"]`
- **Redirect URIs**: Automatically configured based on the service's base UI URL or base URL

### Security Note

The `allowPublicClientWithoutPKCE` parameter should remain `false` (default) in production environments. PKCE (Proof Key for Code Exchange) is required for public clients per OAuth 2.0 Security Best Current Practice. Only enable this setting for testing or backward compatibility with legacy clients.

## User Management

### Adding Users to PAM Issuer

For security reasons, the PAM issuer manages its own users within its container. This isolation ensures that authentication credentials are contained within the PAM issuer service and do not affect the host system.

To add a user to the PAM issuer:

1. **Create a new user:**

```bash
sudo podman exec flightctl-pam-issuer adduser <USER>
```

1. **Set the user's password:**

```bash
sudo podman exec flightctl-pam-issuer passwd <USER>
```

**Example:**

```bash
# Add user 'alice'
sudo podman exec flightctl-pam-issuer adduser alice

# Set password for 'alice'
sudo podman exec flightctl-pam-issuer passwd alice
```

You will be prompted to enter and confirm the password.

### Using Host System Users (Advanced)

If you prefer to use your host system's existing users instead of managing users within the PAM issuer container, you can configure System Security Services Daemon (SSSD) integration.

**Requirements:**

1. **Mount the SSSD pipe** - The SSSD communication pipe must be mounted from the host into the PAM issuer container. Add the following line to the `[Container]` section of the `/etc/containers/systemd/flightctl-pam-issuer.container` file:

```text
Volume=/var/lib/sss/pipes:/var/lib/sss/pipes:rw
```

1. **Configure the host SSSD service** - Ensure that your host's SSSD configuration (`/etc/sssd/sssd.conf`) and NSSwitch (`/etc/nsswitch.conf`) include the `files` provider. This allows PAM to authenticate against both local user files and SSSD-managed users.

1. **Configure the container NSSwitch** - The PAM issuer container's `/etc/nsswitch.conf` must also include the `files` provider.

Example `/etc/nsswitch.conf` configuration (both host and container):

```text
passwd:     files sss
shadow:     files sss
group:      files sss
```

Consult your system's SSSD documentation for detailed configuration steps specific to your environment.

## Security Considerations

### User Isolation

By default, the PAM issuer maintains its own user database within the container. This provides:

- **Isolation**: Authentication credentials are separate from the host system
- **Security**: Compromised PAM issuer credentials do not affect host system access
- **Portability**: User management is self-contained within the service

### Password Management

- Use strong passwords for all PAM issuer users
- Regularly rotate passwords following your organization's security policies
- Consider implementing password complexity requirements through PAM configuration

### Access Control

- Only grant PAM issuer access to users who require Flight Control administrative capabilities
- Review user access regularly and remove accounts that are no longer needed
- Monitor authentication logs for suspicious activity

## Additional Resources

For technical details about the PAM issuer architecture and deployment configurations, see the developer documentation:

- [PAM Issuer Architecture](../developer/pam-issuer-architecture.md)
- [PAM Issuer Deployment Guide](../developer/pam-issuer-deployment.md)

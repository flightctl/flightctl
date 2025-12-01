# CLI Command Reference

This document provides a reference for Flight Control CLI commands.

For procedures and examples, see [Using the CLI](../using/cli/overview.md).

## flightctl login

Authenticate to a Flight Control service.

### Synopsis

```shell
flightctl login <server_url> [flags]
```

### Arguments

* `<server_url>` - URL of the Flight Control API server

### Flags

#### Token Authentication

* `-t, --token <token>` - Bearer token for authentication

#### Provider-Based Authentication

* `--provider <name>` - Name of the authentication provider to use
* `--show-providers` - List available authentication providers
* `--auth-certificate-authority <path>` - Path to CA certificate file for the authentication server

#### OAuth/OIDC Flow Flags

* `--web` - Use OAuth/OIDC authorization code flow (opens browser)
* `-u, --username <username>` - Username for OAuth/OIDC password flow
* `-p, --password <password>` - Password for OAuth/OIDC password flow
* `--callback-port <port>` - Port for OAuth/OIDC callback (default: 8080)

#### TLS/Certificate Flags

* `--certificate-authority <path>` - Path to CA certificate file for the API server
* `-k, --insecure-skip-tls-verify` - Skip TLS certificate verification

#### Other Flags

* `-h, --help` - Display help

### Configuration File

* Default location: `~/.config/flightctl/client.yaml`

### Exit Status

* `0` - Success
* Non-zero - Error

## See Also

* [Using the CLI](../using/cli/overview.md)
* [Logging in to the Service](../using/cli/logging-in.md)

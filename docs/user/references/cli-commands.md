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

---

## flightctl logs

Print the logs for a resource.

### Synopsis

```shell
flightctl logs (TYPE/NAME | TYPE NAME) [flags]
```

### Arguments

* `TYPE/NAME` or `TYPE NAME` - Resource type and name. Supported types:
  * `imagebuild` - Logs from an ImageBuild resource
  * `imageexport` - Logs from an ImageExport resource

### Flags

* `-f, --follow` - Stream logs in real-time until the build/export completes or the command is interrupted

### Examples

```shell
# Get logs for an imagebuild
flightctl logs imagebuild/my-build

# Follow logs for an active imagebuild
flightctl logs imagebuild/my-build -f

# Get logs for an imageexport
flightctl logs imageexport/my-export

# Follow logs for an active imageexport
flightctl logs imageexport/my-export -f
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl download

Download a resource artifact.

### Synopsis

```shell
flightctl download TYPE/NAME OUTPUT_FILE
```

### Arguments

* `TYPE/NAME` - Resource type and name. Supported types:
  * `imageexport` - Download the exported disk image from an ImageExport resource
* `OUTPUT_FILE` - Path to the output file where the artifact will be saved

### Description

Downloads the disk image artifact from a completed ImageExport resource. The command displays download progress and prompts for confirmation if the output file already exists.

### Examples

```shell
# Download an exported qcow2 image
flightctl download imageexport/my-export ./my-image.qcow2

# Download an exported ISO image
flightctl download imageexport/my-iso-export ./install.iso
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## See Also

* [Using the CLI](../using/cli/overview.md)
* [Logging in to the Service](../using/cli/logging-in.md)
* [Managing Image Builds and Exports](../using/managing-image-builds.md)

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

#### Credential File

* `--credentials-file <path>` - Path to a JSON file containing credentials (`token`, `username`, `password`). Takes precedence over flags and environment variables.

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

#### Environment Variables

* `FLIGHTCTL_TOKEN` - Bearer token (used when `--token` is not set)
* `FLIGHTCTL_USERNAME` - Username (used when `-u` is not set)
* `FLIGHTCTL_PASSWORD` - Password (used when `-p` is not set)

Precedence: `--credentials-file` > CLI flags > environment variables. See [Logging in Non-Interactively](../using/cli/logging-in.md#logging-in-non-interactively-automation-and-scripting).

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
flightctl download (TYPE/NAME | TYPE NAME) OUTPUT_FILE
```

### Arguments

* `TYPE/NAME` or `TYPE NAME` - Resource type and name. Supported types:
  * `imageexport` - Download the exported disk image from an ImageExport resource
* `OUTPUT_FILE` - Path to the output file where the artifact will be saved

### Description

Downloads the disk image artifact from a completed ImageExport resource. The command displays download progress and prompts for confirmation if the output file already exists.

### Examples

```shell
# Download an exported qcow2 image (using TYPE/NAME form)
flightctl download imageexport/my-export ./my-image.qcow2

# Download an exported ISO image (using TYPE NAME form)
flightctl download imageexport my-iso-export ./install.iso
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl cancel

Cancel a running resource operation.

### Synopsis

```shell
flightctl cancel TYPE/NAME
flightctl cancel TYPE NAME
```

### Arguments

* `TYPE/NAME` or `TYPE NAME` - Resource type and name (both forms are accepted). Supported types:
  * `imagebuild` - Cancel a running ImageBuild
  * `imageexport` - Cancel a running ImageExport

### Description

Initiates a graceful cancellation of a running ImageBuild or ImageExport operation. The resource status will transition to `Canceling` while the operation is being stopped, and then to `Canceled` once complete.

Only resources in an active state can be canceled:

* **ImageBuild**: `Pending`, `Building`, or `Pushing`
* **ImageExport**: `Pending`, `Converting`, or `Pushing`

Resources that have already completed, failed, or been canceled cannot be canceled again.

### Examples

```shell
# Cancel a running imagebuild (using TYPE/NAME form)
flightctl cancel imagebuild/my-build

# Cancel a running imageexport (using TYPE NAME form)
flightctl cancel imageexport my-export
```

### Exit Status

* `0` - Success
* Non-zero - Error (resource not found, not cancelable, etc.)

---

## Managing enrollment hook policies

Manage the enrollment hook policy used when devices are approved for enrollment.

### Synopsis

```shell
flightctl apply -f FILENAME [flags]
flightctl get enrollmenthookpolicies [flags]
flightctl get enrollmenthookpolicy/default [flags]
flightctl edit enrollmenthookpolicy/default [flags]
flightctl delete enrollmenthookpolicy/default [flags]
```

### Arguments

* `FILENAME` - YAML or JSON file containing an `EnrollmentHookPolicy` resource with `apiVersion: flightctl.io/v1beta1` and `metadata.name: default`.
* Resource type - `enrollmenthookpolicy`, `enrollmenthookpolicies`, or the short name `ehp`.
* Resource name - `default`, the only supported policy name. Commands also accept the `TYPE NAME` form, such as `flightctl get ehp default`.

### Flags

| Command | Flag | Description |
|---------|------|-------------|
| `apply` | `-f, --filename <policy_file>` | File containing the policy to create or replace |
| `apply` | `--dry-run` | Print the resource without sending it to the service |
| `get` | `-o, --output <format>` | Output format: `json`, `yaml`, `wide`, or `name`; the default is a table |
| `edit` | `-o, --output <format>` | Format used in the editor: `yaml` (default) or `json` |
| `edit` | `--editor <editor>` | Editor to use; otherwise, select it from `FLIGHTCTL_EDITOR`, `VISUAL`, or `EDITOR`, falling back to `vi` |
| All | `--org <organization_id>` | Use the specified organization instead of the organization in your client configuration |

### Description

Each organization can have one policy, named `default`. The policy applies to future enrollment approvals throughout that organization.

Apply a policy only after all agents that might enroll in the organization have been upgraded to version 1.4 or later. See [Using enrollment hooks](../using/enrollment-hooks.md) for prerequisites and policy examples.

Approval saves a snapshot of the policy on the device. Editing or deleting the policy does not change settings for devices already approved. Deleting it disables `AfterEnrolling` hooks and webhook notifications for future approvals.

### Examples

Create or replace the policy from a file:

```shell
flightctl apply -f enrollment-hooks.yaml
```

List the policy in your organization:

```shell
flightctl get enrollmenthookpolicies
```

Display the policy in YAML using its short name:

```shell
flightctl get ehp/default -o yaml
```

Edit the policy in your default editor:

```shell
flightctl edit enrollmenthookpolicy/default
```

Delete the policy:

```shell
flightctl delete enrollmenthookpolicy/default
```

### Exit status

* `0` - Success
* Non-zero - Error, such as an invalid policy, a missing policy, or insufficient permissions

---

## Managing label sync mappings

Manage the mappings that derive labels for a resource type from a CEL expression.

### Synopsis

```shell
flightctl apply -f FILENAME [flags]
flightctl get labelsyncmappings [flags]
flightctl get labelsyncmapping/NAME [flags]
flightctl edit labelsyncmapping/NAME [flags]
flightctl delete labelsyncmapping/NAME [flags]
```

### Arguments

* `FILENAME` - YAML or JSON file containing a `LabelSyncMapping` resource with `apiVersion: flightctl.io/v1beta1`.
* Resource type - `labelsyncmapping`, `labelsyncmappings`, or the short name `lsm`.
* Resource name - the name of the mapping. Commands also accept the `TYPE NAME` form, such as `flightctl get lsm my-mapping`.

### Flags

| Command | Flag | Description |
|---------|------|-------------|
| `apply` | `-f, --filename <mapping_file>` | File containing the mapping to create or replace |
| `apply` | `--dry-run` | Print the resource without sending it to the service |
| `get` | `-o, --output <format>` | Output format: `json`, `yaml`, `wide`, or `name`; the default is a table |
| `get` | `--limit <n>` | Maximum number of mappings returned in the list response |
| `get` | `--continue <token>` | Continue a previous list request from the returned token |
| `edit` | `-o, --output <format>` | Format used in the editor: `yaml` (default) or `json` |
| `edit` | `--editor <editor>` | Editor to use; otherwise, select it from `FLIGHTCTL_EDITOR`, `VISUAL`, or `EDITOR`, falling back to `vi` |
| All | `--org <organization_id>` | Use the specified organization instead of the organization in your client configuration |

### Description

Applying a mapping creates it when it does not exist and replaces it otherwise. Editing a mapping sends a JSON patch computed from your edits, so concurrent changes made by someone else are detected and rejected.

The label sync mappings list endpoint supports pagination only. Unlike most other resources, it accepts neither `-l, --selector` nor `--field-selector`, and you can request at most one mapping by name at a time.

In the default table output, the `EXPRESSION` column is shortened to 50 characters and ends with an ellipsis when the expression is longer. Use `-o yaml` or `-o json` to see the complete expression.

### Examples

Create or replace a mapping from a file:

```shell
flightctl apply -f labelsyncmapping.yaml
```

List the mappings in your organization:

```shell
flightctl get labelsyncmappings
```

Display one mapping in YAML using its short name:

```shell
flightctl get lsm/my-mapping -o yaml
```

Edit a mapping in your default editor:

```shell
flightctl edit labelsyncmapping/my-mapping
```

Delete a mapping:

```shell
flightctl delete labelsyncmapping/my-mapping
```

### Exit status

* `0` - Success
* Non-zero - Error, such as an invalid expression, a missing mapping, or insufficient permissions

---

## flightctl get vulnerability

View vulnerability information for devices and fleets.

### Synopsis

```shell
flightctl get vulnerability [SUBCOMMAND] [flags]
flightctl get vuln [SUBCOMMAND] [flags]
```

### Arguments

The vulnerability command supports several forms:

| Form | Description |
|------|-------------|
| `get vuln` | List all known vulnerabilities across the fleet |
| `get vuln --summary-only` | Show a summary of vulnerability counts by severity |
| `get vuln device/<name>` | List vulnerabilities affecting a specific device |
| `get vuln device/<name> --summary` | Show summary followed by vulnerability list for a device |
| `get vuln device/<name> --summary-only` | Show only the vulnerability summary for a device |
| `get vuln fleet/<name>` | List vulnerabilities affecting devices in a fleet |
| `get vuln fleet/<name> --summary` | Show summary followed by vulnerability list for a fleet |
| `get vuln fleet/<name> --summary-only` | Show only the vulnerability summary for a fleet |
| `get vuln <CVE-ID>` | Show impact details for a specific CVE |
| `get vuln vuln/<CVE-ID>` | Show impact details for a specific CVE (alternate form) |

### Flags

* `--summary` - Include a summary before the vulnerability list (for device or fleet queries)
* `--summary-only` - Show only the summary without the full vulnerability list
* `--sort-by <field>` - Sort results by a field. Allowed values depend on the subcommand:
  * For list: `cveId`, `severity`, `cvssScore`, `publishedAt`
  * For device/fleet: `cveId`, `severity`, `cvssScore`, `publishedAt`
  * For CVE impact: `affectedDevices`, `fleetName`
* `--order <direction>` - Sort order: `asc` or `desc`
* `--limit <n>` - Maximum number of results to return
* `--continue <token>` - Pagination token from a previous response
* `--field-selector <selector>` - Filter results by field values
* `-o, --output <format>` - Output format: `json`, `yaml`, `wide`, or `table` (default)

### Examples

```shell
# List all vulnerabilities
flightctl get vuln

# Show vulnerability summary
flightctl get vuln --summary-only

# List vulnerabilities for a device
flightctl get vuln device/my-device

# Show device vulnerabilities with summary
flightctl get vuln device/my-device --summary

# Show only device vulnerability summary
flightctl get vuln device/my-device --summary-only

# List vulnerabilities for a fleet
flightctl get vuln fleet/production

# Show fleet vulnerabilities sorted by severity
flightctl get vuln fleet/production --sort-by severity --order desc

# Show impact details for a specific CVE
flightctl get vuln CVE-2023-44487

# Show CVE impact in JSON format
flightctl get vuln CVE-2023-44487 -o json
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## Filtering devices by CVE

Filter devices by a specific CVE using the `--cve-id` flag.

### Synopsis

```shell
flightctl get devices --cve-id <CVE-ID> [flags]
```

### Description

The `--cve-id` flag filters the device list to show only devices affected by a specific CVE. This is useful for identifying the blast radius of a vulnerability across your fleet.

### Flags

* `--cve-id <CVE-ID>` - Filter devices by CVE ID (e.g., `CVE-2023-44487`)

This flag can be combined with other `get devices` flags such as `--selector`, `--field-selector`, and `--output`.

### Examples

```shell
# List all devices affected by a specific CVE
flightctl get devices --cve-id CVE-2023-44487

# List affected devices with wide output
flightctl get devices --cve-id CVE-2023-44487 -o wide

# Find fleetless devices affected by a CVE
flightctl get devices --cve-id CVE-2023-44487 --field-selector "metadata.owner notcontains Fleet/"

# Find affected devices in a specific region
flightctl get devices --cve-id CVE-2023-44487 --selector region=us-west
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl app start

Start an application running on a device, or on every device owned by a fleet.

### Synopsis

```shell
flightctl app start (device/NAME | fleet/NAME) --name APP [flags]
```

### Arguments

* `device/NAME` or `fleet/NAME` - Target device or fleet

### Flags

* `--name <app_name>` - Application name to control (required)
* `-y, --yes` - Skip the confirmation prompt

### Description

Requests that the named application be started on the target device, or on every device owned by the target fleet.

If the application is already started, the request still succeeds; the application stays running and no restart is performed. Starting on a fleet applies to every current member device and to devices that join the fleet later. A later start or stop issued directly against a device takes precedence for that device over the fleet-wide setting.

### Examples

```shell
# Start an application on a single device
flightctl app start device/my-device --name my-app

# Start an application on every device in a fleet
flightctl app start fleet/my-fleet --name my-app

# Skip the confirmation prompt
flightctl app start device/my-device --name my-app --yes
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl app stop

Stop an application running on a device, or on every device owned by a fleet.

### Synopsis

```shell
flightctl app stop (device/NAME | fleet/NAME) --name APP [flags]
```

### Arguments

* `device/NAME` or `fleet/NAME` - Target device or fleet

### Flags

* `--name <app_name>` - Application name to control (required)
* `-y, --yes` - Skip the confirmation prompt

### Description

Requests that the named application be stopped on the target device, or on every device owned by the target fleet.

If the application is already stopped, the request still succeeds; the application stays stopped. Stopping on a fleet applies to every current member device and to devices that join the fleet later. A later start or stop issued directly against a device takes precedence for that device over the fleet-wide setting.

### Examples

```shell
# Stop an application on a single device
flightctl app stop device/my-device --name my-app

# Stop an application on every device in a fleet
flightctl app stop fleet/my-fleet --name my-app

# Skip the confirmation prompt
flightctl app stop device/my-device --name my-app --yes
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl app restart

Restart an application running on a device.

### Synopsis

```shell
flightctl app restart device/NAME --name APP [flags]
```

### Arguments

* `device/NAME` - Target device

### Flags

* `--name <app_name>` - Application name to control (required)
* `-y, --yes` - Skip the confirmation prompt

### Description

Requests that the named application be restarted on the target device. Restart is only supported on individual devices; fleets are not supported.

The command can be issued whether or not the application is currently running. A restart only takes effect while the application is supposed to be running; if the application has been stopped, the request still succeeds but no restart is performed.

### Examples

```shell
# Restart an application on a device
flightctl app restart device/my-device --name my-app
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## flightctl app console

Connect a console to a VM application running on a device through the server.

### Synopsis

```shell
flightctl app console device/NAME --name APP --type serial|vnc [flags]
```

### Arguments

* `device/NAME` - Target device

### Flags

* `--name <app_name>` - Application name to open a console for (required)
* `--type <serial|vnc>` - Console type (required)
* `--tty` - Allocate a remote pseudo terminal
* `--notty` - Don't allocate a remote pseudo terminal (mutually exclusive with `--tty`)
* `--exposed-port <port>` - Local TCP port for the VNC port-forward (`0` = random ephemeral port; only valid with `--type vnc`)
* `--force` - Take over an already-active console session of the same `--type` for the same `--name`, disconnecting it

### Description

Only devices can be targeted; fleets are not supported. Requires a remote access service configured in the client config, which is set automatically by `flightctl login`.

A `serial` console bridges stdin/stdout over a WebSocket connection to the device's agent, the same way as `flightctl console`; use `~.` to disconnect.

A `vnc` console opens a local TCP listener and bridges a single VNC viewer connection through the agent to the application's VNC server. The session ends once that viewer disconnects; run the command again to start a new session.

Only one console session of a given type (serial or VNC) is allowed per application at a time; a serial and a VNC session may be open on the same application simultaneously. If a console session of the same type for the same application is already active, the command fails with a conflict error unless `--force` is passed, which disconnects that same-type session.

### Examples

```shell
# Connect to an application's serial console
flightctl app console device/my-device --name my-app --type serial

# Connect to an application's VNC console
flightctl app console device/my-device --name my-app --type vnc

# Request a specific local port for the VNC port-forward
flightctl app console device/my-device --name my-app --type vnc --exposed-port 5900

# Take over an already-active console session
flightctl app console device/my-device --name my-app --type serial --force
```

### Exit Status

* `0` - Success
* Non-zero - Error

---

## See Also

* [Using the CLI](../using/cli/overview.md)
* [Logging in to the Service](../using/cli/logging-in.md)
* [Managing Application Lifecycle](../using/managing-devices.md#managing-application-lifecycle)
* [Accessing a VM Application Console](../using/managing-devices.md#accessing-a-vm-application-console)
* [Managing Image Builds and Exports](../using/managing-image-builds.md)
* [Viewing Vulnerabilities](../using/viewing-vulnerabilities.md)

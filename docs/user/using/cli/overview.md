# Using the Flight Control CLI

The Flight Control CLI (`flightctl`) is a command-line tool for interacting with the Flight Control service. It provides commands for managing devices, fleets, repositories, and other resources.

## Before You Begin

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* You have credentials to access a Flight Control service

## Authenticating to the Service

Before running CLI commands, you must authenticate to the Flight Control service. See [Logging into the Flight Control Service](logging-in.md) for detailed authentication procedures.

Quick start:

```shell
flightctl login https://flightctl.example.com --web
```

## Working with Resources

### Viewing Resources

To display resources, use the `get` command:

```shell
# Get all devices
flightctl get devices

# Get a specific device
flightctl get device my-device

# Get all fleets
flightctl get fleets
```

For detailed information about managing devices and fleets, see:

* [Managing Devices](../managing-devices.md)
* [Managing Fleets](../managing-fleets.md)

### Creating and Updating Resources

To create or update resources from files, use the `apply` command:

```shell
# Apply a configuration from a file
flightctl apply -f fleet.yaml

# Apply all YAML files in a directory
flightctl apply -f ./configurations/
```

### Deleting Resources

To delete resources, use the `delete` command:

```shell
# Delete a specific fleet
flightctl delete fleet my-fleet

# Delete a device
flightctl delete device my-device
```

### Editing Resources

To edit a resource interactively, use the `edit` command:

```shell
# Edit a fleet configuration
flightctl edit fleet my-fleet

# Edit a device configuration
flightctl edit device my-device
```

## Customizing Output

### Output Formats

Most commands support multiple output formats:

```shell
# Default table output (human-readable)
flightctl get devices

# JSON output
flightctl get devices -o json

# YAML output
flightctl get devices -o yaml

# Wide output with additional columns
flightctl get devices -o wide
```

### Filtering with Selectors

Filter resources using label selectors:

```shell
# Get devices with specific labels
flightctl get devices --selector environment=production

# Get devices matching multiple labels
flightctl get devices --selector environment=production,region=us-west
```

See [Field Selectors](../field-selectors.md) for more information on filtering resources.

## Using Global Flags

The following flags are available for most commands:

* `--config <path>` - Path to the config file (default: `~/.config/flightctl/client.yaml`)
* `--server <url>` - Server URL (overrides config file)
* `--token <token>` - Bearer token for authentication (overrides config file)
* `--insecure-skip-tls-verify` - Skip TLS certificate verification (not recommended for production)
* `-o, --output <format>` - Output format: `json`, `yaml`, `wide`, or `table`
* `-h, --help` - Display help information
* `-v, --verbose` - Enable verbose output

## Getting Help

To get help for any command, use the `--help` flag:

```shell
# General CLI help
flightctl --help

# Help for a specific command
flightctl login --help
flightctl get --help
```

For complete command syntax and flags, see the [CLI Command Reference](../../references/cli-commands.md).

## Next Steps

* [Logging in to the Service](logging-in.md) - Authenticate to Flight Control
* [Managing Devices](../managing-devices.md) - Work with individual devices
* [Managing Fleets](../managing-fleets.md) - Work with device fleets
* [Troubleshooting](../troubleshooting.md) - Solve common issues

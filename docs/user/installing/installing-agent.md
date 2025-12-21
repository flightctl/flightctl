# Configuring the Flight Control Agent

When the `flightctl-agent` starts, it reads its configuration from `/etc/flightctl/config.yaml` as well as a number of drop-in directories:

* `/etc/flightctl/conf.d/`: Drop-in directory for the agent configuration.
* `/etc/flightctl/hooks.d/`: Drop-in directory for device lifecycle hooks. Overrides hooks of the same name under `/usr/lib/flightctl/hooks.d`.
* `/usr/lib/flightctl/hooks.d/`: Drop-in directory for device lifecycle hooks.
* `/usr/lib/flightctl/custom-info.d/`: Drop-in directory for custom system info collectors.

## Agent `config.yaml` configuration file

The agent's configuration file `/etc/flightctl/config.yaml` takes the following parameters:

| Parameter | Type | Required | Description|
| --------- | ---- | :------: |------------|
| `enrollment-service`     | `EnrollmentService` | Y | Connection details for the device owner's Flight Control service used by the agent to enroll the device. |
| `spec-fetch-interval`    | `Duration` | | **Deprecated**: This parameter is no longer used. The agent now uses long-polling to receive specification updates immediately when available. |
| `status-update-interval` | `Duration` | | Interval in which the agent reports its device status under normal conditions. The agent immediately sends status reports on major events related to the health of the system and application workloads as well as on the progress during a system update. Default: `60s` |
| `default-labels`         | `object` (`string`) | | Labels (`key: value`-pairs) that the agent requests for the device during enrollment. Default: `{}` |
| `system-info`            | `array` (`string`) | | System info that the agent shall include in status updates from built-in collectors. See [Built-in system info collectors](#built-in-system-info-collectors). Default: `["hostname", "kernel", "distroName", "distroVersion", "productName", "productUuid", "productSerial", "netInterfaceDefault", "netIpDefault", "netMacDefault"]` |
| `system-info-custom`     | `array` (`string`) | | System info that the agent shall include in status updates from user-defined collectors. See [Custom system info collectors](#custom-system-info-collectors). Default: `[]` |
| `system-info-timeout`    | `Duration` | | The timeout for collecting system info. Default: `2m`. Maximum: `2m` |
| `pull-timeout`           | `Duration` | | The timeout for pulling a single OCI target. Default: `10m` |
| `log-level`              | `string` | | The level of logging: "panic", "fatal", "error", "warn"/"warning", "info", "debug", or "trace". Default: `info` |
| `metrics-enabled`        | `boolean` | | Enable Prometheus metrics endpoint. See [Metrics Configuration](#metrics-configuration). Default: `false` |
| `profiling-enabled`      | `boolean` | | Enable pprof profiling endpoint. See [Profiling Configuration](#profiling-configuration). Default: `false` |
| `audit`                  | `Audit` | | Audit logging configuration. See [Audit Configuration](#audit-configuration). Default: enabled |
| `tpm`                    | `TPM` | | TPM configuration for hardware-based device identity. See [TPM Configuration](#tpm-configuration). Default: TPM disabled |

`Duration` values are strings of an integer value with appended unit of time ('s' for seconds, 'm' for minutes, or 'h' for hours). Examples: `30s`, `10m`, `24h`

> [!NOTE]
> The `/etc/flightctl/conf.d/` drop-in directory supports only a subset of the agent configuration. Currently supported keys include:
> `log-level`, `system-info`, `system-info-custom`, and `system-info-timeout`.

## Communication Timeouts

The agent uses the following internal timeouts when communicating with the Flight Control service:

| Operation | Timeout | Description |
| --------- | ------- | ----------- |
| Spec fetch (long-poll) | 4 minutes | The agent uses long-polling to fetch device specification updates. The server holds the connection open until a new specification is available or the timeout expires. |
| Status update | 60 seconds | Timeout for pushing device status updates to the service. If the update times out, the agent retries on the next status sync interval. |

These timeouts are not configurable. The spec fetch timeout is intentionally long to support efficient long-polling, while the status update timeout is shorter to ensure timely retries within the configured `status-update-interval`.

> [!NOTE]
> If a status update fails (including timeout), the agent preserves the pending status and automatically retries on the next sync cycle. This ensures status updates are eventually delivered even during transient network issues.

## Built-in system info collectors

The agent has a set of built-in collectors for system information. You can see the information collected by these collectors using the following command:

```console
sudo flightctl-agent system-info | jq '.'
```

Out of these, the agent includes a standard set of system infos in its device status:

```console
status:
  [...]
  systemInfo:
    architecture: amd64
    operatingSystem: linux
    agentVersion: v0.7.0
    bootID: 87f7e27e-bdc0-42b1-b909-6dc81fe43ea2
```

You can specify extra system infos to be included in the device status by listing them under the `system-info` configuration parameter:

| System Info           | Description                                                       |
|-----------------------|-------------------------------------------------------------------|
| `hostname`            | The system hostname reported by the device                        |
| `kernel`              | The running Linux kernel version                                  |
| `distroName`          | The name of the operating system distribution.                    |
| `distroVersion`       | The version of the operating system distribution                  |
| `productName`         | The system’s product or model name (from DMI data)                |
| `productSerial`       | The hardware serial number (if available)                         |
| `productUuid`         | The UUID of the system board or chassis                           |
| `biosVendor`          | The vendor of the BIOS or firmware                                |
| `biosVersion`         | The version of the BIOS or firmware                               |
| `netInterfaceDefault` | The name of the default network interface                         |
| `netIpDefault`        | The first usable IP address of the default network interface      |
| `netMacDefault`       | The MAC address of the default network interface                  |
| `gpu`                 | Lists detected GPU devices                                        |
| `memoryTotalKb`       | Total memory (RAM) in kilobytes                                   |
| `cpuCores`            | Number of physical CPU cores detected                             |
| `cpuProcessors`       | Number of logical processors detected                             |
| `cpuModel`            | CPU vendor and model name                                         |

For example, if you add the following parameter to your agent's `config.yaml`

```console
system-info: [hostname, kernel, distroName, distroVersion]
```

then the reported device status might look like

```console
status:
  [...]
  systemInfo:
    architecture: amd64
    operatingSystem: linux
    agentVersion: v0.7.0
    bootID: 87f7e27e-bdc0-42b1-b909-6dc81fe43ea2
    hostname: device.example.com
    kernel: 5.14.0-503.38.1.el9_5.x86_64
    distroName: Red Hat Enterprise Linux
    distroVersion: 9.5 (Plow)
```

## Custom system info collectors

You can specify custom system info collectors that the agent calls and whose output it includes under `status.systemInfo.customInfo` in the device status.

To add a key `myInfo`,

1. add an executable with that name to `/usr/lib/flightctl/custom-info.d/` that when it is executed returns the desired value, and
2. enable the collection and reporting of this info by adding the key `myInfo` to the agent's `config.yaml` under the `system-info-custom` configuration parameter.

For example, to have the agent report the system's [FIPS](https://en.wikipedia.org/wiki/FIPS_140-2) mode status, create a file `/usr/lib/flightctl/custom-info.d/fips` with the following content and "executable" file permissions:

```bash
#!/bin/sh

fips-mode-setup --is-enabled
case $? in
    0) echo "enabled";;
    1) echo "inconsistent";;
    2) echo "disabled";;
    *) echo "unknown";;
esac
```

Then, add the following to the agent's `config.yaml`:

```console
system-info-custom: [fips]
```

The reported device status might look like

```console
status:
  [...]
  systemInfo:
    architecture: amd64
    operatingSystem: linux
    agentVersion: v0.7.0
    bootID: 87f7e27e-bdc0-42b1-b909-6dc81fe43ea2
    customInfo:
      fips: disabled
```

## Audit Configuration

The audit configuration controls whether the agent generates audit logs that track device specification changes and system state transitions. Audit logs are written to `/var/log/flightctl/audit.log` in JSONL format and are automatically rotated.

### Audit Configuration Parameters

The `audit` configuration object accepts the following parameter:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `enabled` | `boolean` | | Enable audit logging. When true, the agent records specification transitions to the audit log. Default: `true` |

### Example Audit Configuration

Audit logging is enabled by default. To explicitly disable it:

```yaml
# /etc/flightctl/config.yaml
[...]
audit:
  enabled: false

status-update-interval: 60s
```

For detailed information about audit logs, see [Device Observability](../using/device-observability.md#agent-audit-logs).

## Metrics Configuration

Metrics are **disabled by default**. To enable the Prometheus metrics endpoint (exposed on `127.0.0.1:15690`):

```yaml
# /etc/flightctl/config.yaml
[...]
metrics-enabled: true

status-update-interval: 60s
```

For detailed information about agent metrics, see [Device Observability](../using/device-observability.md#agent-metrics).

## Profiling Configuration

Profiling is **disabled by default**. To enable the pprof profiling endpoint (exposed on `127.0.0.1:15689`):

```yaml
# /etc/flightctl/config.yaml
[...]
profiling-enabled: true

status-update-interval: 60s
```

For detailed information about agent profiling, see [Device Observability](../using/device-observability.md#agent-profiling).

## TPM Configuration

The Trusted Platform Module (TPM) configuration allows the agent to use hardware-based device identity and authentication. When enabled, the agent uses the TPM 2.0 module to generate and protect cryptographic keys, providing a hardware root-of-trust for device authentication.

### TPM Configuration Parameters

The `tpm` configuration object accepts the following parameters:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `enabled` | `boolean` | | Enable TPM device identity. When true, the agent uses TPM for key generation and protection. Default: `false` |
| `device-path` | `string` | | Path to the TPM device. If not specified, the agent auto-discovers available TPM devices, preferring resource manager devices (`/dev/tpmrm*`) over direct devices. Default: auto-discovery |
| `auth-enabled` | `boolean` | | Enable TPM owner hierarchy password authentication. Should only be used in ephemeral development/test environments. Default: `false` |
| `storage-file-path` | `string` | | File path for TPM key handle persistence. Default: `/var/lib/flightctl/tpm-blob.yaml` |

### Example TPM Configuration

```yaml
# /etc/flightctl/config.yaml
[...]
tpm:
  enabled: true
  device-path: /dev/tpm0
  auth-enabled: false
  storage-file-path: /var/lib/flightctl/tpm-blob.yaml

status-update-interval: 60s
```

### TPM Auto-Discovery

When `device-path` is not specified or is empty, the agent automatically discovers TPM devices by:

1. Scanning `/sys/class/tpm/` for available TPM devices
2. Validating TPM version (must be 2.0)
3. Preferring resource manager devices (`/dev/tpmrm*`) over direct devices (`/dev/tpm*`)
4. Using the first valid TPM 2.0 device found

### TPM Requirements

* **Hardware**: TPM 2.0 compliant module (TPM 1.2 is not supported)
* **Kernel**: Linux kernel with TPM 2.0 support
* **Permissions**: Agent must have read/write access to TPM device
* **CA Certificates**: TPM manufacturer CA certificates must be installed on the Flight Control service

For detailed information about TPM authentication architecture and certificate requirements, see [TPM Device Authentication](configuring-device-attestation.md).

### TPM Troubleshooting

#### TPM Not Detected

If the agent cannot detect the TPM device:

```bash
# Check if TPM device exists
ls -la /dev/tpm* /dev/tpmrm*

# Verify TPM version (should output: 2)
cat /sys/class/tpm/tpm0/tpm_version_major

# Check device permissions
ls -l /dev/tpm0
# Agent user should have read/write access
```

#### TPM Initialization Errors

Check agent logs for TPM-related errors:

```bash
# View agent logs
journalctl -u flightctl-agent -f

# Filter for TPM messages
journalctl -u flightctl-agent | grep -i tpm
```

Enable debug logging for detailed TPM information:

```yaml
# /etc/flightctl/config.yaml
log-level: debug  # or trace for maximum detail
```

#### TPM Owner Password

If the TPM requires owner hierarchy authentication:

```yaml
tpm:
  enabled: true
  auth-enabled: true  # Generates random password for TPM ownership
```

> [!WARNING]
> Setting `auth-enabled: true` generates a random password for TPM ownership that is stored in the storage file. This password survives reboots, but if the storage file is lost, the ability to use the ownership hierarchy is lost and a `tpm2_clear` command must be issued to reset the TPM. Only use this in ephemeral development environments.

#### Enrollment Failures

If enrollment fails with TPM-related errors:

1. Check that TPM CA certificates are installed on the server (see [TPM Device Authentication](configuring-device-attestation.md#configuring-tpm-ca-certificates-in-flight-control-service))

2. Verify the agent can generate CSR:

   ```bash
   sudo flightctl-agent --log-level=trace
   # Look for CSR generation messages
   ```

## flightctl-agent system-info

You can run this command on a device to inspect the full system information collected by the agent:

```bash
flightctl-agent system-info
```

It prints a JSON summary of the device's hardware and OS, including:

* Architecture, OS, kernel, hostname
* CPU, memory, disks, network interfaces
* GPU (if available)
* BIOS/system identifiers
* Custom info (from user-defined collectors)
* Boot ID and time

> [!NOTE]
> This command is local-only and does not affect device state or communicate with the Flight Control service.

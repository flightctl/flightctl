# Configuring the Flight Control Agent

When the `flightctl-agent` starts, it reads its configuration from `/etc/flightctl/config.yaml` as well as a number of drop-in directories:

* `/etc/flightctl/conf.d/`: Drop-in directory for the agent configuration.
* `/etc/flightctl/hooks.d/`: Drop-in directory for device lifecycle hooks. Overrides hooks of the same name under `/usr/lib/flightctl/hooks.d`.
* `/usr/lib/flightctl/hooks.d/`: Drop-in directory for device lifecycle hooks.
* `/usr/lib/flightctl/custom-info.d/`: Drop-in directory for custom system info collectors.

## Agent `config.yaml` configuration file

The agent's configuration file `/etc/flightctl/config.yaml` takes the following parameters:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `enrollment-service` | `EnrollmentService` | Y | Connection details for the device owner's Flight Control service used by the agent to enroll the device. |
| `spec-fetch-interval` | `Duration` | | Interval in which the agent polls the service for updates to its device specification. Default: `60s` |
| `status-update-interval` | `Duration` | | Interval in which the agent reports its device status under normal conditions. The agent immediately sends status reports on major events related to the health of the system and application workloads as well as on the progress during a system update. Default: `60s` |
| `default-labels` | `object` (`string`) | | Labels (`key: value`-pairs) that the agent requests for the device during enrollment. Default: `{}` |
| `system-info` | `array` (`string`) | | System info that the agent shall include in status updates from built-in collectors. See [Built-in system info collectors](#built-in-system-info-collectors). Default: `["hostname", "kernel", "distroName", "distroVersion", "productName", "productUuid", "productSerial", "netInterfaceDefault", "netIpDefault", "netMacDefault"]` |
| `system-info-custom` | `array` (`string`) | | System info that the agent shall include in status updates from user-defined collectors. See [Custom system info collectors](#custom-system-info-collectors). Default: `[]` |
| `system-info-timeout` | `Duration` | | The timeout for collecting system info. Default: `2m` |
| `log-level` | `string` | | The level of logging: "panic", "fatal", "error", "warn"/"warning", "info", "debug", or "trace". Default: `info` |

`Duration` values are strings of an integer value with appended unit of time ('s' for seconds, 'm' for minutes, or 'h' for hours). Examples: `30s`, `10m`, `24h`

> [!NOTE]
> The `/etc/flightctl/conf.d/` drop-in directory supports only a subset of the agent configuration. Currently supported keys include:
> `log-level`, `system-info`, `system-info-custom`, and `system-info-timeout`.

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

## flightctl-agent system-info

You can run this command on a device to inspect the full system information collected by the agent:

```bash
flightctl-agent system-info
```

It prints a JSON summary of the device’s hardware and OS, including:

* Architecture, OS, kernel, hostname
* CPU, memory, disks, network interfaces
* GPU (if available)
* BIOS/system identifiers
* Custom info (from user-defined collectors)
* Boot ID and time

> [!NOTE]
> This command is local-only and does not affect device state or communicate with the Flight Control service.

### GPU Hardware Mapping via hardware-map.yaml

The agent supports enriching GPU details using a PCI vendor and model mapping file. This optional file allows human-readable names, memory sizes, architecture, and feature tags to be associated with specific GPU devices.

Example hardware-map.yaml
```yaml
- vendorID: "0x10de"
  vendorName: "NVIDIA Corporation"
  models:
    - pciID: "0x1b80"
      pciName: "GeForce GTX 1080"
      memoryBytes: 8589934592  # 8 GiB
      architecture: "Pascal"
      features: ["cuda", "tensor-core"]
```
Usage
Save the file as /etc/flightctl/hardware-map.yaml (default) or another accessible path.

Pass alternative path for `hardware-map` if desired:
```sh
flightctl-agent system-info --hardware-map /alt/path/hardware-map.yaml
```
The GPU section in the system info will be enriched:

```json
"gpu": [
  {
    "index": 0,
    "vendor": "NVIDIA Corporation",
    "model": "GeForce GTX 1080",
    "vendorId": "0x10de",
    "deviceId": "0x1b80",
    "memoryBytes": 8589934592,
    "architecture": "Pascal",
    "features": ["cuda", "tensor-core"]
  }
]
```
>[!NOTE]
>If no mapping file is provided, the agent uses only sysfs to gather GPU data, which may yield less detail.

Finding PCI Vendor and Device IDs
To look up vendor and device IDs for your GPU:

Run the following to list PCI devices with vendor and device IDs:

```sh
lspci -nn | grep -i vga
```
Example output:

```yaml
01:00.0 VGA compatible controller [0300]: NVIDIA Corporation GP104 [GeForce GTX 1080] [10de:1b80]
```
Here, `10de` is the Vendor ID, and `1b80` is the Device ID.

You can look up IDs and names online at the canonical [PCI ID database](https://pci-ids.ucw.cz/read/PC).

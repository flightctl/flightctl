# Device Audit Logs

Flight Control agents automatically generate audit logs to track device specification changes and system state transitions. These logs provide a tamper-evident record of what changes occurred, when, and why.

## Overview

Audit logs capture all specification transitions on edge devices, including:

- **Bootstrap events** – Initial spec creation during device enrollment
- **Sync events** – Successful application of desired spec to current state
- **Upgrade events** – OS or application updates
- **Rollback events** – Reverting to a previous known-good configuration
- **Recovery events** – Automated recovery from failed states

Each audit event is written as a single JSON line (JSONL format) to `/var/log/flightctl/audit.log` on the device. This format enables efficient parsing, streaming, and integration with log aggregation systems.

## Audit Event Structure

Each audit event is a JSON object with the following fields:

| Field                    | Type   | Description                                                           |
|--------------------------|--------|-----------------------------------------------------------------------|
| `ts`                     | string | Event timestamp in RFC3339 format (e.g., `2024-11-19T12:00:00Z`)    |
| `device`                 | string | Device identifier                                                     |
| `old_version`            | string | Previous spec version (empty for bootstrap)                           |
| `new_version`            | string | New spec version                                                      |
| `result`                 | string | Outcome (success or failure)                                          |
| `reason`                 | string | Why the change occurred (bootstrap, sync, upgrade, rollback, etc.)   |
| `type`                   | string | Spec type affected (current, desired, or rollback)                   |
| `fleet_template_version` | string | Fleet template version                   |
| `agent_version`          | string | Version of the Flight Control agent that generated the event         |

### Example Audit Event

```json
{
  "ts": "2024-11-19T12:34:56Z",
  "device": "edge-device-01",
  "old_version": "5",
  "new_version": "6",
  "result": "success",
  "reason": "sync",
  "type": "current",
  "fleet_template_version": "42",
  "agent_version": "0.1.0"
}
```

## Audit Event Types

### Event Reasons

| Reason            | Description                                                              |
|-------------------|--------------------------------------------------------------------------|
| `bootstrap`       | Initial spec creation during device enrollment or first boot             |
| `sync`            | Desired spec successfully applied to current state                       |
| `upgrade`         | OS or application version upgrade                                        |
| `rollback`        | Revert to previous known-good configuration after failure                |
| `recovery`        | Automated recovery from failed or inconsistent state                     |
| `initialization`  | Agent initialization or restart                                          |

### Spec Types

| Type       | Description                                                                   |
|------------|-------------------------------------------------------------------------------|
| `current`  | The actual running state of the device                                        |
| `desired`  | The target state the device should achieve                                    |
| `rollback` | The last known-good state, used for recovery if desired state fails          |

### Results

| Result    | Description                                                                     |
|-----------|---------------------------------------------------------------------------------|
| `success` | Operation completed successfully (only value in current MVP implementation)     |
| `failure` | Operation failed (planned for future releases)                                  |

> [!NOTE]
> Failure auditing is planned for future releases. Current implementation logs only successful state transitions.

## Bootstrap Events

When a device enrolls or first starts, the agent creates three bootstrap events:

1. **Current spec bootstrap** – Records initial running state
2. **Desired spec bootstrap** – Records target configuration
3. **Rollback spec bootstrap** – Creates initial recovery point

All bootstrap events have:

- `reason`: `bootstrap`
- `old_version`: empty string
- `new_version`: `"0"`
- `result`: `success`

Example bootstrap sequence:

```json
{"ts":"2024-11-19T10:00:00Z","device":"dev-01","old_version":"","new_version":"0","result":"success","reason":"bootstrap","type":"current","fleet_template_version":"","agent_version":"0.1.0"}
{"ts":"2024-11-19T10:00:01Z","device":"dev-01","old_version":"","new_version":"0","result":"success","reason":"bootstrap","type":"desired","fleet_template_version":"","agent_version":"0.1.0"}
{"ts":"2024-11-19T10:00:02Z","device":"dev-01","old_version":"","new_version":"0","result":"success","reason":"bootstrap","type":"rollback","fleet_template_version":"","agent_version":"0.1.0"}
```

## Viewing Audit Logs

### On the Device

View audit logs directly on the device:

```bash
# View the audit log file
sudo cat /var/log/flightctl/audit.log

# Follow audit events in real-time
sudo tail -f /var/log/flightctl/audit.log

# View through systemd journal
sudo journalctl -u flightctl-agent | grep -i audit
```

### Parsing with jq

Since audit logs use JSONL format, they're easily parsed with `jq`.

**Prerequisites:**

- Root or sudo access to the device
- `jq` installed (typically available by default on RHEL-based systems)

### Basic Filtering

View all events in readable format:

```bash
sudo cat /var/log/flightctl/audit.log | jq .
```

Filter by reason (e.g., show only sync events):

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "sync")'
```

**Example output:**

```json
{
  "ts": "2024-11-19T14:22:15Z",
  "device": "c860jhsr1uj96sugum9csciop392mbnmsjrslpv0r7s22i76misg",
  "old_version": "5",
  "new_version": "6",
  "result": "success",
  "reason": "sync",
  "type": "current",
  "fleet_template_version": "42",
  "agent_version": "v1.0.0-main-322-g16f0ccea"
}
```

Filter by spec type:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.type == "current")'
```

### Advanced Filtering

Filter by multiple conditions (e.g., successful upgrades only):

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "upgrade" and .result == "success")'
```

Filter by version transitions (e.g., find specific upgrade path):

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.old_version == "5" and .new_version == "6")'
```

Exclude bootstrap events:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason != "bootstrap")'
```

Filter by time range (events from the last hour):

```bash
sudo cat /var/log/flightctl/audit.log | jq --arg cutoff "$(date -u -d '1 hour ago' '+%Y-%m-%dT%H:%M:%SZ')" 'select(.ts > $cutoff)'
```

### Aggregation and Analysis

Count events by reason:

```bash
sudo cat /var/log/flightctl/audit.log | jq -r '.reason' | sort | uniq -c
```

**Example output:**

```text
      3 bootstrap
     15 sync
      2 upgrade
      1 rollback
```

Count events by device and reason:

```bash
sudo cat /var/log/flightctl/audit.log | jq -r '[.device, .reason] | @tsv' | sort | uniq -c
```

Get latest 5 events:

```bash
sudo tail -5 /var/log/flightctl/audit.log | jq .
```

### Custom Output Format

Extract only specific fields:

```bash
sudo cat /var/log/flightctl/audit.log | jq '{ts, device, reason, old_version, new_version}'
```

Create a compact timeline (TSV format):

```bash
sudo cat /var/log/flightctl/audit.log | jq -r '[.ts, .device, .reason, .type, .old_version + "->" + .new_version] | @tsv'
```

**Example output:**

```text
2024-11-19T10:00:00Z    dev-01    bootstrap    current    ->0
2024-11-19T10:15:23Z    dev-01    sync    current    0->1
2024-11-19T11:30:45Z    dev-01    upgrade    desired    1->2
```

### Integration with Log Aggregation

Audit logs can be shipped to centralized logging systems using standard log forwarders:

#### Example: Fluentd Configuration

```conf
<source>
  @type tail
  path /var/log/flightctl/audit.log
  pos_file /var/log/flightctl/audit.log.pos
  tag flightctl.audit
  <parse>
    @type json
  </parse>
</source>

<match flightctl.audit>
  @type forward
  <server>
    host log-aggregator.example.com
    port 24224
  </server>
</match>
```

#### Example: Promtail Configuration (Loki)

```yaml
scrape_configs:
  - job_name: flightctl-audit
    static_configs:
      - targets:
          - localhost
        labels:
          job: flightctl-audit
          __path__: /var/log/flightctl/audit.log
```

## Configuration

Audit logging is **enabled by default** and requires no configuration. To explicitly control it, modify the agent configuration:

### Enabling Audit Logging

In `/etc/flightctl/config.yaml`:

```yaml
audit:
  enabled: true
```

### Disabling Audit Logging

```yaml
audit:
  enabled: false
```

> [!WARNING]
> Disabling audit logs removes visibility into device state changes and may impact compliance requirements.

## Log Location and Permissions

| Property     | Value                              |
|--------------|------------------------------------|
| **Path**     | `/var/log/flightctl/audit.log` (hardcoded, not configurable) |
| **Format**   | JSONL (one JSON object per line)   |

The directory `/var/log/flightctl` is created automatically by the agent when writing the first audit event.

## Automatic Log Rotation

Audit logs use **automatic size-based rotation** built into the agent (using lumberjack). No external log rotation configuration is needed.

### Rotation Settings

These settings are hardcoded and not user-configurable:

| Setting         | Value                                      |
|-----------------|--------------------------------------------|
| **Max Size**    | 1 MB per file |
| **Max Backups** | 3 backup files                             |
| **Max Age**     | No time-based pruning                      |
| **Compression** | Enabled (rotated files are compressed)     |
| **Total Space** | ≈4 MB (1 active + 3 backups)               |
| **Capacity**    | ~3,495 records per file, ~13,980 records total |

### Rotation Behavior

- When `/var/log/flightctl/audit.log` reaches 1 MB, it's automatically rotated
- Current file renamed to `audit.log.1` (compressed to `audit.log.1.gz`)
- Previous backups shift: `audit.log.1` → `audit.log.2` → `audit.log.3`
- Oldest backup (`audit.log.3`) is deleted
- New empty `audit.log` file created
- No agent restart or reload required

## Debugging and Troubleshooting

### Investigating Device State Changes

When a device experiences unexpected behavior, audit logs provide a historical record of state transitions.

#### Scenario: Device reverted to an older configuration

Find all rollback events and identify the version transition:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "rollback")'
```

Check what version the device rolled back from and to:

```bash
sudo cat /var/log/flightctl/audit.log | jq -r 'select(.reason == "rollback") | "Rolled back from \(.old_version) to \(.new_version) at \(.ts)"'
```

**Example output:**

```text
Rolled back from 6 to 5 at 2024-11-19T15:30:00Z
```

#### Scenario: Tracking upgrade progression

Verify an upgrade completed across all spec types:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "upgrade") | {ts, type, old_version, new_version}'
```

#### Scenario: Device not reflecting expected configuration

Check the last successful sync event to see when the device last applied desired state:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "sync" and .type == "current") | {ts, old_version, new_version}' | tail -1
```

#### Scenario: Checking bootstrap history

Verify device enrollment or re-enrollment events:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "bootstrap") | {ts, type, agent_version}'
```

## Gathering Audit Logs for Support

When reporting issues to support or development teams, use one of the diagnostic tools to collect audit logs along with relevant system context. These tools automatically gather audit logs, device specifications, journal logs, and system information.

### Using Diagnostic Tools

#### Option 1: Using must-gather (Recommended)

The `flightctl-must-gather` tool collects comprehensive diagnostic data including audit logs, device specs, and system information.

**Procedure:**

1. Run the `flightctl-must-gather` command:

```bash
sudo flightctl-must-gather
```

The tool will:

- Prompt for confirmation (generates large files)
- Collect journal logs from the last 24 hours
- Copy all FlightCtl device spec files (`/var/lib/flightctl/*.json`)
- Copy all audit logs (`/var/log/flightctl/audit.log*`, including rotated backups)
- Gather system information (uname, disk usage, agent version, bootc status)
- Create a compressed archive: `must-gather-YYYYMMDD-HHMMSS.tgz`

2. Locate the generated archive in the current working directory:

```bash
ls -lh must-gather-*.tgz
```

**Example output:**

```text
-rw-r--r--. 1 root root 2.3M Nov 19 16:45 must-gather-20241119-164512.tgz
```

3. **Copy the archive to your local machine** for analysis or submission:

```bash
scp user@<device-hostname>:/path/to/must-gather-*.tgz .
```

Replace `<device-hostname>` with the actual device hostname or IP address.

4. **Extract and inspect the archive** on your local machine:

```bash
tar -xzf must-gather-20241119-164512.tgz
cd must-gather-*
ls -lh  # View collected files
```

The archive contains:

- `audit.log*` - All audit log files
- `*.json` - Device specs and state files
- System information and journal logs

5. Share the compressed file with your support team or attach it to your support case.

> [!NOTE]
> The must-gather tool requires root privileges and collects data from the last 24 hours by default.

#### Option 2: Using sosreport (sos report)

The `sos report` tool with the FlightCtl plugin provides detailed system diagnostics, including audit logs.

**Prerequisites:**

- `sos` package installed (available in RHEL and Fedora repositories)

**Procedure:**

1. Run `sos report` with the FlightCtl plugin enabled:

```bash
sudo sos report -o flightctl
```

For a specific time range (e.g., last 2 hours):

```bash
sudo sos report -o flightctl -k flightctl.journal_since="2 hours ago"
```

The FlightCtl plugin collects:

- Configuration files from `/etc/flightctl` (excluding certificates)
- Device state and specs from `/var/lib/flightctl` (excluding certificates)
- **All audit logs** from `/var/log/flightctl` (including `audit.log*`)
- Goroutine dumps for debugging
- Performance profiles (heap, CPU)
- Journal logs for `flightctl-agent.service`

2. Wait for `sos report` to complete. The archive location will be displayed:

**Example output:**

```text
Your sos report has been generated and saved in:
  /var/tmp/sosreport-localhost-2025-11-20-bglzmdy.tar.xz
```

3. **Copy the archive to your local machine** for analysis or submission:

```bash
# First, make the file readable (it's owned by root)
sudo chmod 644 /var/tmp/sosreport-*.tar.xz

# Copy to your local machine
scp user@<device-hostname>:/var/tmp/sosreport-*.tar.xz .
```

Replace `<device-hostname>` with the actual device hostname or IP address.

4. **Extract and inspect the archive** on your local machine:

```bash
tar -xJf sosreport-localhost-2025-11-20-bglzmdy.tar.xz
cd sosreport-*/
ls -lh var/log/flightctl/  # View audit logs
```

The archive contains comprehensive system diagnostics including FlightCtl audit logs, configuration, and system state.

5. Share the compressed file with your support team or attach it to the [Red Hat Customer Support](https://access.redhat.com/support/) portal.

> [!TIP]
> Use `sos report` when you need comprehensive system diagnostics beyond just FlightCtl. Use `must-gather` for a lighter-weight, FlightCtl-focused collection.
>
> **Note**: The legacy `sosreport` command is deprecated. Always use `sos report` instead.

#### Option 3: Manual Collection

For quick troubleshooting without diagnostic tools:

```bash
# Collect only audit logs (current + rotated backups)
sudo tar -czf /tmp/flightctl-audit-$(hostname)-$(date +%Y%m%d-%H%M%S).tar.gz \
  /var/log/flightctl/audit.log* 2>/dev/null
```

**To extract and view on your local machine:**

```bash
# Copy from device
scp user@<device-hostname>:/tmp/flightctl-audit-*.tar.gz .

# Extract the archive
tar -xzf flightctl-audit-*.tar.gz

# View the logs
ls -lh var/log/flightctl/
```

Replace `<device-hostname>` with the actual device hostname or IP address.

#### Comparison of Diagnostic Tools

| Tool | Size | Collection Scope | Use Case |
|------|------|------------------|----------|
| **must-gather** | Medium | FlightCtl-focused: audit logs, specs, journal, system info | First-line debugging, FlightCtl-specific issues |
| **sos report** | Large | Comprehensive: all above + system-wide diagnostics | Complex issues, suspected OS/system problems |
| **Manual** | Small | Audit logs only | Quick log review, compliance auditing |

### Validating Audit Log Integrity

Check if audit log is being written correctly:

```bash
# Verify the log file exists and has recent entries
sudo ls -lh /var/log/flightctl/audit.log
sudo stat /var/log/flightctl/audit.log | grep Modify

# Verify JSONL format (each line should be valid JSON)
sudo cat /var/log/flightctl/audit.log | while IFS= read -r line; do
  echo "$line" | jq empty || echo "Invalid JSON found: $line"
done
```

Check log rotation status:

```bash
# List all audit log files (current + rotated backups)
sudo ls -lh /var/log/flightctl/audit.log*
```

**Example output:**

```text
-rw-r--r--. 1 root root 512K Nov 19 15:00 /var/log/flightctl/audit.log
-rw-r--r--. 1 root root 1.0M Nov 19 14:00 /var/log/flightctl/audit.log.1.gz
-rw-r--r--. 1 root root 1.0M Nov 19 13:00 /var/log/flightctl/audit.log.2.gz
-rw-r--r--. 1 root root 1.0M Nov 19 12:00 /var/log/flightctl/audit.log.3.gz
```

## Use Cases

### Compliance and Auditing

Track all configuration changes for compliance requirements.

**Show all upgrades in the last 24 hours:**

```bash
sudo cat /var/log/flightctl/audit.log | jq --arg cutoff "$(date -u -d '24 hours ago' '+%Y-%m-%dT%H:%M:%SZ')" \
  'select(.reason == "upgrade" and .ts > $cutoff)'
```

**Generate compliance report:**

```bash
sudo cat /var/log/flightctl/audit.log | jq -r '[.ts, .device, .reason, .old_version, .new_version, .result] | @csv' > audit-report.csv
```

**To copy the CSV file to your local machine** for analysis:

```bash
scp user@<device-hostname>:~/audit-report.csv .
```

Replace `<device-hostname>` with the actual device hostname or IP address.

### Monitoring Fleet Operations

**Track fleet template version deployment:**

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.fleet_template_version != "") | {ts, fleet_template_version, new_version}'
```

**Count successful vs. failed operations** (when failure auditing is available):

```bash
sudo cat /var/log/flightctl/audit.log | jq -r '.result' | sort | uniq -c
```

### Alerting on Events

Monitor for specific patterns using log aggregation alert rules.

**Example: Loki/Promtail alert rule for rollback events:**

```yaml
- alert: DeviceRolledBack
  expr: count_over_time({job="flightctl-audit"} |~ "rollback"[5m]) > 0
  annotations:
    summary: "Device {{ $labels.device }} performed a rollback"
    description: "Device may have encountered issues requiring rollback to previous configuration"
```

**Example: Alert on frequent upgrades (potential flapping):**

```yaml
- alert: FrequentUpgrades
  expr: count_over_time({job="flightctl-audit"} | json | reason="upgrade" [1h]) > 5
  annotations:
    summary: "Device {{ $labels.device }} upgraded {{ $value }} times in 1 hour"
```

## Quick Reference

### Common Commands

| Task | Command |
|------|---------|
| View audit log | `sudo cat /var/log/flightctl/audit.log` |
| Follow in real-time | `sudo tail -f /var/log/flightctl/audit.log` |
| Pretty print all events | `sudo cat /var/log/flightctl/audit.log \| jq .` |
| Filter by reason | `sudo cat /var/log/flightctl/audit.log \| jq 'select(.reason == "sync")'` |
| Count events by type | `sudo cat /var/log/flightctl/audit.log \| jq -r '.reason' \| sort \| uniq -c` |
| Find rollbacks | `sudo cat /var/log/flightctl/audit.log \| jq 'select(.reason == "rollback")'` |
| Check log rotation | `sudo ls -lh /var/log/flightctl/audit.log*` |
| Gather for support (recommended) | `sudo flightctl-must-gather` |
| Gather with sos report | `sudo sos report -o flightctl` |
| Manual audit log collection | `sudo tar -czf /tmp/audit-logs.tar.gz /var/log/flightctl/audit.log*` |

### Key Facts

- **Default**: Enabled automatically, no configuration required
- **Location**: `/var/log/flightctl/audit.log` (not configurable)
- **Format**: JSONL (one JSON object per line)
- **Rotation**: Automatic, 1 MB per file, 3 backups, ~4 MB total
- **Permissions**: Requires root/sudo access to read
- **Fields**: 9 fields per event (ts, device, old_version, new_version, result, reason, type, fleet_template_version, agent_version)

### Event Reasons Quick Reference

| Reason | When It Occurs |
|--------|----------------|
| `bootstrap` | Device enrollment or first boot |
| `sync` | Desired spec applied successfully |
| `upgrade` | OS or application update |
| `rollback` | Revert to previous configuration |
| `recovery` | Automated recovery from failed state |
| `initialization` | Agent start or restart |

## Additional Resources

- [Configuring the Agent](./configuring-agent.md) - Agent configuration reference including audit settings
- [Managing Devices](./managing-devices.md) - Understanding device specs and state transitions
- [Troubleshooting Guide](./troubleshooting.md) - Common device issues and solutions
- [Device Observability](./device-observability.md) - Monitoring and metrics for devices
- [jq Manual](https://jqlang.github.io/jq/manual/) - Advanced JSON filtering techniques
- [Red Hat sosreport Documentation](https://access.redhat.com/solutions/3592) - Using sosreport for system diagnostics

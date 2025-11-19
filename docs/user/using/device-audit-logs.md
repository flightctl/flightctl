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

Since audit logs use JSONL format, they're easily parsed with `jq`:

```bash
# View all events in readable format
cat /var/log/flightctl/audit.log | jq .

# Filter by reason
cat /var/log/flightctl/audit.log | jq 'select(.reason == "sync")'

# Filter by spec type
cat /var/log/flightctl/audit.log | jq 'select(.type == "current")'

# Count events by reason
cat /var/log/flightctl/audit.log | jq -r '.reason' | sort | uniq -c

# Get latest 5 events
tail -5 /var/log/flightctl/audit.log | jq .

# Extract specific fields
cat /var/log/flightctl/audit.log | jq '{ts, device, reason, old_version, new_version}'
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
| **Max Size**    | 1 MB per file (configured as 300 KB but rounds up to 1 MB due to lumberjack's integer-only API limitation) |
| **Max Backups** | 3 backup files                             |
| **Max Age**     | No time-based pruning                      |
| **Compression** | Enabled (rotated files are compressed)     |
| **Total Space** | ≈4 MB (1 active + 3 backups)               |
| **Capacity**    | ~3,495 records per file, ~13,980 records total |

> [!NOTE]
> The lumberjack library's `MaxSize` field only accepts integer megabytes (no fractional values). The code maintains `DefaultMaxSizeKB = 300` for clarity, but the actual rotation boundary is 1 MB (the minimum allowed by lumberjack's API). This still provides a 50% reduction in footprint compared to the original 2MB/8MB configuration.

### Rotation Behavior

- When `/var/log/flightctl/audit.log` reaches 1 MB, it's automatically rotated
- Current file renamed to `audit.log.1` (compressed to `audit.log.1.gz`)
- Previous backups shift: `audit.log.1` → `audit.log.2` → `audit.log.3`
- Oldest backup (`audit.log.3`) is deleted
- New empty `audit.log` file created
- No agent restart or reload required

## Use Cases

### Compliance and Auditing

Track all configuration changes for compliance requirements:

```bash
# Show all upgrades in the last 24 hours
cat /var/log/flightctl/audit.log | jq -r \
  'select(.reason == "upgrade") | select(.ts > (now - 86400 | strftime("%Y-%m-%dT%H:%M:%SZ")))'
```

### Troubleshooting Rollbacks

Identify what triggered a rollback:

```bash
# Find rollback events
cat /var/log/flightctl/audit.log | jq 'select(.reason == "rollback")'
```

### Change Timeline

Build a timeline of device configuration changes:

```bash
# Show chronological change history
cat /var/log/flightctl/audit.log | jq -r '[.ts, .reason, .type, .old_version, .new_version] | @tsv'
```

### Alerting on Events

Monitor for specific patterns using log aggregation alert rules:

```yaml
# Example: Alert on rollback events
- alert: DeviceRolledBack
  expr: count_over_time({job="flightctl-audit"} |~ "rollback"[5m]) > 0
  annotations:
    summary: "Device {{ $labels.device }} performed a rollback"
```

## Considerations

### Disk Space

- Audit logs are automatically managed with built-in rotation (max ~4 MB total)
- Estimate ~300 bytes per event on average
- A device with 100 spec changes per day generates ~30KB/day (~13,980 events fit in 4 MB capacity)

### Performance

- Audit logging has minimal performance impact (<1% CPU overhead)
- Writes are buffered and asynchronous
- No impact on spec reconciliation timing

### Privacy and Security

- Audit logs may contain device identifiers and version information
- Logs are created by the agent running as root
- Consider encryption when shipping logs off-device
- Ensure compliance with data retention policies

### High Availability

- Audit logs are local to each device
- For centralized auditing, configure log forwarding to external systems
- Consider redundant log storage for critical compliance use cases

## Future Enhancements

The following features are planned for future releases:

- **Failure event logging** – Capture failed state transitions with error details
- **User attribution** – Record which user or system triggered the change
- **Cryptographic signing** – Sign audit events for tamper detection
- **Direct API streaming** – Ship audit events directly to Flight Control service
- **Retention policies** – Configurable on-device retention and automatic cleanup


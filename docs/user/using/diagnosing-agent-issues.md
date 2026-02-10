# Diagnosing Agent Issues

This document describes the Flight Control agent's built-in diagnostic features for monitoring and troubleshooting device behavior.

The agent provides three complementary diagnostic layers:

1. **Metrics Endpoint** - Prometheus-compatible `/metrics` endpoint exposing RPC performance and certificate renewal metrics
2. **Profiling Endpoint** - Go `pprof` endpoints for diagnosing performance issues like high CPU usage or memory growth
3. **Audit Logs** - Structured logs tracking device specification changes and state transitions

All diagnostic features are loopback-only and designed for local debugging and automated collection through SOS reports.

## Monitoring Agent Metrics

The Flight Control agent can expose internal Prometheus `/metrics` to help analyze the agent's performance and behavior.
This endpoint is loopback-only and intended for local debugging and for collection through SOS reports.

### Enabling Metrics

Metrics are disabled by default.
To enable them, add the following setting to the agent's configuration file at `/etc/flightctl/config.yaml`:

```yaml
metrics-enabled: true
```

### Understanding the Metrics Endpoint

When enabled, the agent starts a lightweight HTTP server and exposes a Prometheus-compatible metrics endpoint at: `http://127.0.0.1:15690/metrics`.

### Understanding RPC Metrics

The agent records a histogram for each RPC performed against the Flight Control service.
These metrics allow evaluating performance trends and computing service-level indicators (for example, p95 RPC latency).
Each histogram exposes:

- `create_enrollmentrequest_duration_seconds`
- `get_enrollmentrequest_duration_seconds`
- `get_rendered_device_spec_duration_seconds`
- `update_device_status_duration_seconds`
- `patch_device_status_duration_seconds`
- `create_certificate_signing_request_duration_seconds`
- `get_certificate_signing_request_duration_seconds`

### Understanding Management Certificate Metrics

The agent exposes a small set of metrics that describe the **device management certificate** state and renewal behavior.

#### Current State

- `flightctl_device_mgmt_cert_loaded` (gauge) - `1` when the management certificate is present and loaded successfully; `0` otherwise.
- `flightctl_device_mgmt_cert_not_after_timestamp_seconds` - (gauge) Unix timestamp (seconds) of the currently loaded certificate `NotAfter` (expiration time). `0` if unknown / no cert is loaded.

#### Renewal Flow

All renewal metrics include the label result, one of: success, failure, pending.

- `flightctl_device_mgmt_cert_renewal_attempts_total{result=...}` (counter) - Total number of renewal attempts performed by the agent, partitioned by outcome.
- `flightctl_device_mgmt_cert_renewal_duration_seconds{result=...}` (histogram) - Duration in seconds of a renewal attempt. For successful renewals, this includes the full flow from starting provisioning until the certificate is stored. For pending/failure, it reflects the time spent in the provisioning step.

### Querying Metrics with PromQL

The following examples demonstrate common queries for monitoring agent health and certificate state.

Cert is loaded AND expires in < 7 days:

```sql
(flightctl_device_mgmt_cert_loaded == 1)
and
((flightctl_device_mgmt_cert_not_after_timestamp_seconds - time()) < 7 * 24 * 60 * 60)
```

Renewal attempts happened recently (any outcome):

```sql
sum(increase(flightctl_device_mgmt_cert_renewal_attempts_total[30m])) > 0
```

Attempts increased, but NotAfter did not change in the same window:

```sql
(sum(increase(flightctl_device_mgmt_cert_renewal_attempts_total[30m])) > 0)
unless
(changes(flightctl_device_mgmt_cert_not_after_timestamp_seconds[30m]) > 0)
```

Calculates the 95th percentile of renewal duration:

```sql
histogram_quantile(
  0.95,
  sum by (le, device_id) (
    rate(flightctl_device_mgmt_cert_renewal_duration_seconds_bucket[5m])
  )
)
```

Renewal failures in the last 30m:

```sql
increase(flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}[30m]) > 0
```

### Considerations

- Endpoint is never exposed externally; only loopback.
- SOS report collection attempts to scrape the metrics endpoint when metrics are enabled.

## Collecting Profiling Data

The Flight Control agent can expose Go `pprof` profiling endpoints to help diagnose performance issues such as high CPU usage, memory growth, or excessive goroutine creation.
These endpoints are loopback-only and intended for local debugging and for collection through SOS reports.

### Enabling Profiling

Profiling is disabled by default.
To enable it, add the following setting to the agent's configuration file at `/etc/flightctl/config.yaml`:

```yaml
profiling-enabled: true
```

### Understanding the Profiling Endpoint

When enabled, the agent starts a lightweight HTTP server bound to `127.0.0.1:15689`.

All standard Go pprof handlers are exposed under the `/debug/pprof/` path.

### Understanding Available Profiles

| Profile | Description|
| --------- |------------|
| `/debug/pprof/`          | Index listing all profiles |
| `/debug/pprof/profile`   | CPU profile (?seconds=N, capped by the agent) |
| `/debug/pprof/heap`      | Heap profile (?gc=1 optional) |
| `/debug/pprof/goroutine` | Goroutine dump (?debug=2 for full stacks) |
| `/debug/pprof/allocs`    | Allocation sampling |
| `/debug/pprof/block`     | Blocking events |
| `/debug/pprof/mutex`     | Contended mutex samples |
| `/debug/pprof/trace`     | Execution trace (?seconds=N, capped) |

> [!NOTE]
> To prevent misuse or excessive overhead, the agent enforces CPU profile duration cap to 30 seconds, and execution trace duration cap to 5 seconds.

### Capturing Profiles

Collect a 10-second CPU profile:

```bash
curl http://127.0.0.1:15689/debug/pprof/profile?seconds=10 > cpu.pprof
go tool pprof cpu.pprof
```

Retrieve a verbose goroutine dump:

```bash
curl http://127.0.0.1:15689/debug/pprof/goroutine?debug=2 > goroutines.txt
```

Download a heap profile:

```bash
curl http://127.0.0.1:15689/debug/pprof/heap > heap.pprof
```

### Considerations

- Endpoint is never exposed externally; only loopback.
- Profiling temporarily increases CPU usage; use short durations.
- SOS reports automatically collect heap, goroutine, and a short CPU profile when pprof is enabled.

## Analyzing Agent Audit Logs

The Flight Control agent automatically generates audit logs to track device specification changes and system state transitions. These logs provide a structured record of what changes occurred, when, and why.

### Understanding Audit Events

Audit logs capture all specification transitions, including:

- **Bootstrap events** – Initial spec creation during device enrollment
- **Sync events** – Successful application of desired spec to current state
- **Upgrade events** – OS or application updates
- **Rollback events** – Reverting to a previous known-good configuration
- **Recovery events** – Automated recovery from failed states

> [!NOTE]
> Each audit event is written as a single JSON line (JSONL format) to `/var/log/flightctl/audit.log`. This format enables efficient parsing and streaming. Logs are automatically rotated when reaching 1 MB (keeping 3 compressed backups, ~4 MB total).

### Understanding Audit Event Structure

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

#### Example Audit Event

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

#### Event Reasons

| Reason            | Description                                                              |
|-------------------|--------------------------------------------------------------------------|
| `bootstrap`       | Initial spec creation during device enrollment or first boot             |
| `sync`            | Desired spec successfully applied to current state                       |
| `upgrade`         | OS or application version upgrade                                        |
| `rollback`        | Revert to previous known-good configuration after failure                |
| `recovery`        | Automated recovery from failed or inconsistent state                     |
| `initialization`  | Agent initialization or restart                                          |

#### Spec Types

| Type       | Description                                                                   |
|------------|-------------------------------------------------------------------------------|
| `current`  | The actual running state of the device                                        |
| `desired`  | The target state the device should achieve                                    |
| `rollback` | The last known-good state, used for recovery if desired state fails          |

#### Results

| Result    | Description                                                                     |
|-----------|---------------------------------------------------------------------------------|
| `success` | Operation completed successfully    |
| `failure` | Operation failed                               |

### Understanding Bootstrap Events

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

### Configuring Audit Logging

Audit logging is **enabled by default** and requires no configuration. To disable it, modify the agent configuration:

In `/etc/flightctl/config.yaml`:

```yaml
audit:
  enabled: false
```

### Viewing Audit Logs

Audit logs use JSONL format and can be viewed directly at `/var/log/flightctl/audit.log`. For more advanced queries, see the [jq manual](https://jqlang.github.io/jq/manual/).

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

### Filtering and Querying Audit Logs

Filter by multiple conditions (e.g., successful upgrades only):

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "upgrade" and .result == "success")'
```

Exclude bootstrap events:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason != "bootstrap")'
```

Filter by time range (events from the last hour):

```bash
sudo cat /var/log/flightctl/audit.log | jq --arg cutoff "$(date -u -d '1 hour ago' '+%Y-%m-%dT%H:%M:%SZ')" 'select(.ts > $cutoff)'
```

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

### Debugging Common Scenarios

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
Rolled back from 6 to 5 at 2024-11-19:15:30:00Z
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

### Considerations

- Audit logs are enabled by default with no additional configuration required.
- Both [`flightctl-must-gather`](../installing/installing-service-on-linux.md#must-gather-script) and [`sos report`](troubleshooting.md#generating-and-downloading-an-sos-report) automatically collect audit logs and rotated backups for diagnostic purposes.

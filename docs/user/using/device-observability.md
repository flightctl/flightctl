# Device Observability

This document describes how to enable device observability in Flight Control.

There are two complementary layers of device observability:

1. **Device Telemetry** — **Telemetry Gateway** and a device-side **OpenTelemetry Collector**.
Devices can collect metrics from the host operating system and workloads, enabling correlation of host- and application-level metrics.
Communication between devices and the gateway is secured with **mutual TLS (mTLS)**, with device identity established through certificates issued by the Flight Control Certificate Authority (CA).
2. **Agent Diagnostics** — local observability provided directly by the **Flight Control agent**. The agent exposes local diagnostics, including Prometheus `/metrics`, `pprof` profiling endpoints, and bounded `audit logs` that track device specification changes and system state transitions.

Key components:

- **Telemetry Gateway**  
  Acts as the entry point for all device telemetry. It terminates mTLS, validates device certificates against the Flight Control Certificate Authority (CA), and labels telemetry data with the authenticated `device_id` and `org_id`.

- **Device Management Certificate (agent ↔ service)**  
  The Flight Control agent uses a separate device-specific **management certificate** to authenticate to the Flight Control service for device management operations.

- **Device Observability Certificate**  
  Devices use the Flight Control agent to request a client certificate issued by the Flight Control CA.  
  The certificate, together with its metadata, is used for mTLS connections to the Telemetry Gateway and carries the authenticated `device_id` and `org_id`.

- **Device-side OpenTelemetry Collector**  
  Runs locally on the device to collect telemetry data (e.g., system metrics).  
  The collector uses the device certificate to establish a secure gRPC connection and send telemetry data to the Telemetry Gateway.

- **Agent Metrics Endpoint**  
  The Flight Control agent can expose optional Prometheus `/metrics` endpoint on a loopback-only port.
  It provides timing histograms and counters that describe the agent’s internal operations and behaviour.

- **Agent Profiling Endpoint**  
  The Flight Control agent can expose optional Go profiling endpoints (i.e., `pprof`) on a loopback-only port.  
  These endpoints provide CPU, heap, and goroutine profiles for debugging and performance analysis.

- **Agent Audit Log**  
  The Flight Control agent maintains a bounded, append-only audit log that records changes to the device specification and state transitions.  

## Telemetry Gateway

The Telemetry Gateway is the entry point for device telemetry. It terminates mTLS, validates device certificates against the Flight Control Certificate Authority (CA), and labels telemetry data with the authenticated `device_id` and `org_id`.

> [!NOTE]
> The gateway is not always deployed automatically. How it is deployed depends on the environment.
> If the gateway is already deployed, you can skip the following steps.

### Deployment

For OpenShift or Kubernetes, deploy the gateway using the Helm chart provided in this repository. The chart includes a template you can use as a reference for container arguments, mounts, and configuration. Review the chart for deployment details specific to your environment.

### Certificate requirements

The gateway must have its own server certificate, signed by the Flight Control CA. Devices connecting to it will be authenticated against this CA. You need three files:

- Private key for the gateway
- Server certificate issued by Flight Control CA
- CA certificate to verify device client certificates

#### Generating the server certificate with flightctl

Create a working directory and generate a private key:

```bash
mkdir -p ./certs
chmod 700 ./certs
openssl ecparam -genkey -name prime256v1 -out ./certs/svc-telemetry-gateway.key
chmod 600 ./certs/svc-telemetry-gateway.key
```

Create a CSR with appropriate SANs (adjust DNS/IP to match your environment):

```bash
openssl req -new -key ./certs/svc-telemetry-gateway.key \
-subj "/CN=svc-telemetry-gateway" \
-addext "subjectAltName=DNS:localhost,DNS:flightctl-telemetry-gateway,IP:127.0.0.1" \
-out ./certs/svc-telemetry-gateway.csr
```

Create a CSR YAML for flightctl:

```bash
cat > ./certs/csr.yaml << EOF
apiVersion: flightctl.io/v1beta1
kind: CertificateSigningRequest
metadata:
  name: svc-telemetry-gateway
spec:
  request: $(base64 -w 0 ./certs/svc-telemetry-gateway.csr)
  signerName: flightctl.io/server-svc
  usages: ["clientAuth", "serverAuth", "CA:false"]
  expirationSeconds: 8640000
EOF
```

Apply the CSR and approve it with flightctl:

```bash
./bin/flightctl apply -f ./certs/csr.yaml
./bin/flightctl get csr
./bin/flightctl approve csr/svc-telemetry-gateway
```

Extract the issued certificate and CA:

```bash
CERT_B64="$(./bin/flightctl get csr/svc-telemetry-gateway -o yaml | python3 deploy/scripts/yaml_helpers.py extract ".status.certificate")"
echo "${CERT_B64}" | base64 -d > ./certs/svc-telemetry-gateway.crt

ENR_CA_B64="$(./bin/flightctl enrollmentconfig | python3 deploy/scripts/yaml_helpers.py extract ".enrollment-service.service.certificate-authority-data")"
echo "${ENR_CA_B64}" | base64 -d > ./certs/ca.crt
```

Resulting files:

- `./certs/svc-telemetry-gateway.key` (private key for the gateway)
- `./certs/svc-telemetry-gateway.crt` (server certificate signed by Flight Control CA)
- `./certs/ca.crt` (CA certificate used to verify device client certificates)

### Gateway configurations

Gateway configuration is read from `/root/.flightctl/config.yaml`. The relevant keys are:

- `logLevel` — verbosity of gateway logs (default: info)
- `tls` — paths to the gateway server certificate, private key, and the CA used to verify device client certificates
- `listen` — device-facing OTLP gRPC listener address (default: 0.0.0.0:4317)
- `export` — Prometheus endpoint for local scraping
- `forward` — upstream OTLP destination

**Default configuration:**

```yaml
telemetryGateway:
  logLevel: info
  tls:
    certFile: /etc/telemetry-gateway/certs/server.crt
    keyFile: /etc/telemetry-gateway/certs/server.key
    caCert: /etc/telemetry-gateway/certs/ca.crt
  listen:
    device: 0.0.0.0:4317
  # export: not set by default
  # forward: not set by default
```

> [!NOTE]
> You must set at least one of export or forward.

**Here is a complete configuration example that sets both export (Prometheus) and forward (upstream OTLP with TLS/mTLS):**

```yaml
telemetryGateway:
  logLevel: info

  # Server-side TLS (device-facing mTLS on :4317)
  tls:
    certFile: /etc/telemetry-gateway/certs/server.crt
    keyFile:  /etc/telemetry-gateway/certs/server.key
    caCert:   /etc/telemetry-gateway/certs/ca.crt

  # Device-facing OTLP gRPC listener
  listen:
    device: 0.0.0.0:4317

  # Option A: expose metrics for local Prometheus scraping
  export:
    prometheus: 0.0.0.0:9464   # Prometheus will scrape http://<host>:9464/metrics

  # Option B: forward telemetry upstream over OTLP/gRPC with TLS (and optional mTLS)
  forward:
    endpoint: otlp.your-backend:4317
    tls:
      insecureSkipTlsVerify: false            # set true only for testing
      caFile:  /etc/telemetry-gateway/certs/upstream-ca.crt   # trust store for the upstream
      # If upstream requires mTLS, provide a client cert/key for the gateway:
      certFile: /etc/telemetry-gateway/certs/upstream-client.crt
      keyFile:  /etc/telemetry-gateway/certs/upstream-client.key
```

### Considerations

- The `forward.endpoint` must be an `OTLP/gRPC` endpoint.
- The gateway will connect with TLS and validate the upstream’s certificate against the CA you specify in `caFile`.
- If the upstream requires **mutual TLS (mTLS)**, you must also provide a client certificate and key (`certFile` and `keyFile`).
- The option `insecureSkipTlsVerify: true` should only be used in development or testing.

## Provision the device client certificate

The agent's cert-manager issues a client certificate using the specified signer and writes it to the OpenTelemetry Collector paths.
Place this in `/etc/flightctl/certs.yaml` (or as a drop-in under `/etc/flightctl/certs.d/*.yaml`):

```yaml
- name: otel
  provisioner:
    type: csr
    config:
      signer: "flightctl.io/device-svc-client"
      common-name: "otel-{{.DEVICE_ID}}"
  storage:
    type: filesystem
    config:
      cert-path: "/etc/otelcol/certs/otel.crt"
      key-path:  "/etc/otelcol/certs/otel.key"
```

### Considerations

- You can either include this file in your `bootc` image so it's present at first boot, or add it later and reload the agent to apply changes (e.g., `systemctl reload flightctl-agent`).
- The directory (e.g., `/etc/otelcol/certs`) must exist and be readable by the OpenTelemetry Collector process; the agent will create the cert and key with secure permissions.
- For the `flightctl.io/device-svc-client` signer, the Common Name must include the device ID. The `{{.DEVICE_ID}}` template is resolved by the agent at runtime and is required; do not replace it with a static Common Name.

> [!NOTE]
> When using bootc, be aware of how `/etc` is managed across upgrades.
> See [bootc documentation](https://bootc-dev.github.io/bootc/filesystem.html#etc) for details.

> [!IMPORTANT]
> Certificates provisioned by the agent via `certs.yaml` (including this OpenTelemetry client certificate)
> are **automatically renewed** by the `flightctl-agent` before expiration.

## Deploy OpenTelemetry Collector on the device

This shows a `bootc` based device image that installs flightctl-agent and OpenTelemetry Collector, provisions the device client certificate into the OpenTelemetry Collector paths, and configures it to send host metrics to the Telemetry Gateway over mTLS (gRPC 4317). A systemd unit is included to start the collector only after the agent has written the cert/key.

### Example: `bootc` image with OpenTelemetry Collector configured to send host metrics to the Telemetry Gateway over mTLS (gRPC)

```yaml
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent opentelemetry-collector && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service

ADD config.yaml /etc/flightctl/
ADD ca.crt /etc/otelcol/certs/ca.crt

# Agent cert-manager mapping -> otelcol cert/key paths
RUN tee /etc/flightctl/certs.yaml >/dev/null <<'CSR'
- name: otel
  provisioner:
    type: csr
    config:
      signer: "flightctl.io/device-svc-client"
      common-name: "otel-{{.DEVICE_ID}}"
  storage:
    type: filesystem
    config:
      cert-path: "/etc/otelcol/certs/otel.crt"
      key-path:  "/etc/otelcol/certs/otel.key"
CSR

# Minimal OTEL config
RUN tee /etc/otelcol/config.yaml >/dev/null <<'OTEL'
receivers:
  hostmetrics:
    collection_interval: 10s
    scrapers: { cpu: {}, memory: {} }
exporters:
  otlp:
    endpoint: telemetry.192.168.1.150.nip.io:4317
    tls:
      ca_file:   /etc/otelcol/certs/ca.crt
      cert_file: /etc/otelcol/certs/otel.crt
      key_file:  /etc/otelcol/certs/otel.key
      insecure:  false
service:
  pipelines:
    metrics:
      receivers: [hostmetrics]
      exporters: [otlp]
OTEL

# ---- minimal otelcol systemd unit (waits for cert+key) ----
RUN mkdir -p /usr/lib/systemd/system
RUN cat > /usr/lib/systemd/system/otelcol.service <<'UNIT'
[Unit]
Description=OpenTelemetry Collector (device addon)
After=network-online.target flightctl-agent.service
Wants=network-online.target
[Service]
Type=simple
ExecStartPre=/bin/sh -lc 'for i in $(seq 1 120); do [ -s /etc/otelcol/certs/otel.crt ] && [ -s /etc/otelcol/certs/otel.key ] && exit 0; sleep 1; done; exit 1'
ConditionPathExists=/etc/otelcol/config.yaml
ExecStart=/usr/bin/opentelemetry-collector  --config=/etc/otelcol/config.yaml
Restart=on-failure
[Install]
WantedBy=multi-user.target
UNIT

RUN systemctl enable otelcol.service
```

- Replace `telemetry.192.168.1.150.nip.io:4317` with your actual gateway endpoint.
- The `flightctl.io/device-svc-client` signer requires the Common Name to include the device ID; keep common-name: `otel-{{.DEVICE_ID}}`.
- Directory `/etc/otelcol/certs` must exist and be readable by the OpenTelemetry Collector; the agent creates the cert/key with secure permissions.
- Startup ordering is handled by the unit (`After=flightctl-agent.service` + `ExecStartPre` wait loop) to avoid `file not found` on first boot.
- Ensure `/etc/otelcol/certs/ca.crt` matches the CA that signs the gateway's server certificate.

One way to extract the CA:

```bash
./bin/flightctl enrollmentconfig \
  | python3 deploy/scripts/yaml_helpers.py extract ".enrollment-service.service.certificate-authority-data" \
  | base64 -d > /etc/otelcol/certs/ca.crt
chmod 644 /etc/otelcol/certs/ca.crt
```

### Considerations

- At the moment, certificates issued by the agent do not support changing the file owner - make sure the OpenTelemetry Collector can read them.
- Please refer to [Building Images](../building/building-images.md) for more details on building your own OS images.

> [!TIP]  
> Instead of baking the OpenTelemetry Collector configuration into the image, you can also deliver it through the Fleet spec.  
> See [Managing OS Configuration](managing-devices.md#managing-os-configuration) and [Device Templates](managing-fleets.md#defining-device-templates).

## Agent Metrics Endpoint

The Flight Control agent can expose internal Prometheus `/metrics` to help analyze the agent’s performance and behaviour.
This endpoint is loopback-only and intended for local debugging and for collection through SOS reports.

### Enable Metrics

Metrics are disabled by default.
To enable them, add the following setting to the agent’s configuration file at `/etc/flightctl/config.yaml`:

```yaml
metrics-enabled: true
```

### Metrics Endpoint

When enabled, the agent starts a lightweight HTTP server and exposes a Prometheus-compatible metrics endpoint at: `http://127.0.0.1:15690/metrics`.

### RPC Metrics

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

### Management Certificate Metrics

The agent exposes a small set of metrics that describe the **device management certificate** state and renewal behaviour.

#### Current state

- `flightctl_device_mgmt_cert_loaded` (gauge) - `1` when the management certificate is present and loaded successfully; `0` otherwise.
- `flightctl_device_mgmt_cert_not_after_timestamp_seconds` - (gauge) Unix timestamp (seconds) of the currently loaded certificate `NotAfter` (expiration time). `0` if unknown / no cert is loaded.

#### Renewal flow

All renewal metrics include the label result, one of: success, failure, pending.

- `flightctl_device_mgmt_cert_renewal_attempts_total{result=...}` (counter) - Total number of renewal attempts performed by the agent, partitioned by outcome.
- `flightctl_device_mgmt_cert_renewal_duration_seconds{result=...}` (histogram) - Duration in seconds of a renewal attempt. For successful renewals, this includes the full flow from starting provisioning until the certificate is stored. For pending/failure, it reflects the time spent in the provisioning step.

#### Example PromQL Queries

```sql
# Cert is loaded AND expires in < 7 days
(flightctl_device_mgmt_cert_loaded == 1)
and
((flightctl_device_mgmt_cert_not_after_timestamp_seconds - time()) < 7 * 24 * 60 * 60)
```

```sql
# Renewal attempts happened recently (any outcome)
sum(increase(flightctl_device_mgmt_cert_renewal_attempts_total[30m])) > 0
```

```sql
# Attempts increased, but NotAfter did not change in the same window.
(sum(increase(flightctl_device_mgmt_cert_renewal_attempts_total[30m])) > 0)
unless
(changes(flightctl_device_mgmt_cert_not_after_timestamp_seconds[30m]) > 0)
```

```sql
# Calculates the 95th percentile of renewal duration.
histogram_quantile(
  0.95,
  sum by (le, device_id) (
    rate(flightctl_device_mgmt_cert_renewal_duration_seconds_bucket[5m])
  )
)
```

```sql
# Renewal failures in the last 30m.
increase(flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}[30m]) > 0
```

### Considerations

- Endpoint is never exposed externally; only loopback.
- SOS report collection attempts to scrape the metrics endpoint when metrics are enabled.

## Agent Profiling Endpoint

The Flight Control agent can expose Go `pprof` profiling endpoints to help diagnose performance issues such as high CPU usage, memory growth, or excessive goroutine creation.
These endpoints are loopback-only and intended for local debugging and for collection through SOS reports.

### Enable Profiling

Profiling is disabled by default.
To enable it, add the following setting to the agent’s configuration file at `/etc/flightctl/config.yaml`:

```yaml
profiling-enabled: true
```

### Profiling Endpoint

When enabled, the agent starts a lightweight HTTP server bound to `127.0.0.1:15689`.

All standard Go pprof handlers are exposed under the `/debug/pprof/` path.

Available Profiles:

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

### Example Usage

```bash
# Collect a 10-second CPU profile:
curl http://127.0.0.1:15689/debug/pprof/profile?seconds=10 > cpu.pprof
go tool pprof cpu.pprof
```

```bash
# Retrieve a verbose goroutine dump:
curl http://127.0.0.1:15689/debug/pprof/goroutine?debug=2 > goroutines.txt
```

```bash
# Download a heap profile:
curl http://127.0.0.1:15689/debug/pprof/heap > heap.pprof
```

### Considerations

- Endpoint is never exposed externally; only loopback.
- Profiling temporarily increases CPU usage; use short durations.
- SOS reports automatically collect heap, goroutine, and a short CPU profile when pprof is enabled.

## Agent Audit Logs

The Flight Control agent automatically generates audit logs to track device specification changes and system state transitions. These logs provide a structured record of what changes occurred, when, and why.

### Overview

Audit logs capture all specification transitions, including:

- **Bootstrap events** – Initial spec creation during device enrollment
- **Sync events** – Successful application of desired spec to current state
- **Upgrade events** – OS or application updates
- **Rollback events** – Reverting to a previous known-good configuration
- **Recovery events** – Automated recovery from failed states

> [!NOTE]
> Each audit event is written as a single JSON line (JSONL format) to `/var/log/flightctl/audit.log`. This format enables efficient parsing and streaming. Logs are automatically rotated when reaching 1 MB (keeping 3 compressed backups, ~4 MB total).

### Audit Event Structure

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

### Audit Event Types

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

### Bootstrap Events

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

### Configuration

Audit logging is **enabled by default** and requires no configuration. To disable it, modify the agent configuration:

#### Disabling Audit Logging

In `/etc/flightctl/config.yaml`:

```yaml
audit:
  enabled: false
```

### Viewing and Analyzing Audit Logs

Audit logs use JSONL format and can be viewed directly at `/var/log/flightctl/audit.log`. For more advanced queries, see the [jq manual](https://jqlang.github.io/jq/manual/).

#### Examples

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

#### Debugging Scenarios

##### Scenario: Device reverted to an older configuration

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

##### Scenario: Tracking upgrade progression

Verify an upgrade completed across all spec types:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "upgrade") | {ts, type, old_version, new_version}'
```

##### Scenario: Device not reflecting expected configuration

Check the last successful sync event to see when the device last applied desired state:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "sync" and .type == "current") | {ts, old_version, new_version}' | tail -1
```

##### Scenario: Checking bootstrap history

Verify device enrollment or re-enrollment events:

```bash
sudo cat /var/log/flightctl/audit.log | jq 'select(.reason == "bootstrap") | {ts, type, agent_version}'
```

### Considerations

- Audit logs are enabled by default with no additional configuration required.
- Both [`flightctl-must-gather`](../installing/installing-service-on-linux.md#must-gather-script) and [`sos report`](troubleshooting.md#generating-and-downloading-an-sos-report) automatically collect audit logs and rotated backups for diagnostic purposes.

# Service Observability

This guide explains how to enable and configure observability in a Flightctl deployment.

Flightctl provides service-level observability to help operators:

- Trace inter-service and asynchronous workflows
- Debug errors and latency issues
- Monitor request flow and system behavior

## Tracing

Flightctl supports distributed tracing via [OpenTelemetry](https://opentelemetry.io/), providing comprehensive visibility into service interactions, database queries, and background task execution.

### Enabling Tracing

Tracing is configured in your `config.yaml`:

```yaml
tracing:
  enabled: true
  endpoint: localhost:4318  # optional
  insecure: false           # optional
```

- `enabled` **(required)**: Enables or disables tracing.
- `endpoint` **(optional)**: The OTLP HTTP endpoint for exporting traces. Defaults to [https://localhost:4318/v1/traces](https://localhost:4318/v1/traces) if not specified.
- `insecure` **(optional)**: Set to true to allow HTTP (non-TLS) connections—useful in development environments.

> [!NOTE]
> All OpenTelemetry-related configuration—including endpoints, protocols, batching, and internal behavior—can be defined or overridden using standard [OpenTelemetry environment variables](https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/protocol/exporter.md).

#### Viewing Traces Locally with Jaeger (Podman)

To inspect Flightctl traces visually, you can run a local Jaeger instance using Podman:

```bash
# Jaeger UI: http://localhost:16686
# OTLP HTTP receiver: http://localhost:4318
podman run --rm --network host jaegertracing/all-in-one:latest
```

> [!NOTE]
> You can use the default Flightctl tracing configuration with `insecure: true` to send traces to this Jaeger instance over HTTP.

Once the container is running, open your browser and navigate to [http://localhost:16686](http://localhost:16686)

This brings up the Jaeger web interface, where you can:

- Search for traces by service name
- View spans and their durations
- Inspect attributes, logs, and errors
- Analyze request flow and timing across services and queues

## Profiling (pprof)

Flightctl can expose Go `pprof` endpoints on loopback for CPU and memory flamegraphs. Profiling is **disabled by default**.

Configure in `config.yaml` (for example `/etc/flightctl/service-config.yaml` on quadlets):

```yaml
profiling:
  enabled: true
  # port: 15691   # optional override for a single process only
```

One `profiling.enabled: true` in the shared service config turns pprof on for every long-running service that loads that config. Each process binds a **different default port** so they do not conflict on one host. Only set `profiling.port` when running a single service and you need a custom port.

| Process | Default port |
|---------|-------------:|
| Agent (`profiling-enabled` in agent config) | 15689 |
| `flightctl-api` | 15691 |
| `flightctl-worker` | 15692 |
| `flightctl-periodic` | 15693 |
| `flightctl-alert-exporter` | 15694 |
| `flightctl-alertmanager-proxy` | 15695 |
| `flightctl-remote-access` | 15696 |
| `flightctl-imagebuilder-api` | 15697 |
| `flightctl-imagebuilder-worker` | 15698 |
| `flightctl-telemetry-gateway` | 15699 |
| `flightctl-pam-issuer` | 15700 |

Endpoints are `http://127.0.0.1:<port>/debug/pprof/`. The server listens on loopback only. In containers, capture from inside the container (for example `podman exec`).

Capture a CPU profile while reproducing the issue, then open a flamegraph:

```bash
curl -sS "http://127.0.0.1:15692/debug/pprof/profile?seconds=30" -o worker-cpu.pb.gz
go tool pprof -http=:0 worker-cpu.pb.gz
```

> [!NOTE]
> Leaving the pprof listener enabled has negligible cost. Capturing a CPU profile samples the process and adds overhead for the duration of the capture. Prefer a short capture window and a modest device count when profiling rollouts; do not use a full CPT-scale run solely for flamegraphs.

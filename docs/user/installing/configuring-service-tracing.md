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

## Profiling

Flightctl supports two independent profiling backends under `profiling`. Either, both, or neither may be enabled. Both are **disabled by default**.

For Podman/quadlet deployments, uncomment the `profiling` block in `/etc/flightctl/service-config.yaml` (see `deploy/podman/service-config.yaml`), then re-render or restart the services so each `config.yaml` picks it up. For Helm, set `dev.profiling` (for example in `values.dev.yaml`).

```yaml
profiling:
  pprof:
    enabled: true
    # port: 15691   # optional; only for a single process override
  pyroscope:
    enabled: true
    serverAddress: http://pyroscope:4040
    # applicationName: flightctl-worker   # optional; defaults to the process name
    # basicAuthUser: ...
    # basicAuthPassword: ...
    # tenantID: ...
```

### pprof (pull / on-demand)

Starts a loopback-only Go `net/http/pprof` server. Use for manual captures and flamegraphs with `go tool pprof`.

One `profiling.pprof.enabled: true` in the shared service config turns pprof on for every long-running service that loads that config. Each process binds a **different default port** so they do not conflict on one host. Only set `profiling.pprof.port` when running a single service and you need a custom port.

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

Endpoints are `http://127.0.0.1:<port>/debug/pprof/`. In containers, capture from inside the container (for example `podman exec`).

```bash
curl -sS "http://127.0.0.1:15692/debug/pprof/profile?seconds=30" -o worker-cpu.pb.gz
go tool pprof -http=:0 worker-cpu.pb.gz
```

Leaving the pprof listener enabled has negligible cost. Capturing a CPU profile samples the process for the duration of the capture.

### pyroscope (push / continuous)

When `profiling.pyroscope.enabled` is true, each process pushes CPU and memory profiles to a Grafana Pyroscope server (`serverAddress` is required). Profiles appear in the Pyroscope UI (flame graphs, memory, diffs over time).

`applicationName` defaults to the process name (`flightctl-api`, `flightctl-worker`, …). Optional `basicAuthUser` / `basicAuthPassword` / `tenantID` support Grafana Cloud or multi-tenant Pyroscope.

Continuous push adds ongoing sampling overhead. Prefer pprof for short local captures; use Pyroscope when you need history across a CPT or long run.

On startup the service logs which backend it starts, for example:

- `profiling: starting pprof on loopback port 15692`
- `profiling: starting pyroscope push to http://pyroscope:4040 as "flightctl-worker"`

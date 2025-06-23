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
- `endpoint` **(optional)**: The OTLP HTTP endpoint for exporting traces. Defaults to `https://localhost:4318/v1/traces` if not specified.
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

Once the container is running, open your browser and navigate to: <http://localhost:16686>

This brings up the Jaeger web interface, where you can:

- Search for traces by service name
- View spans and their durations
- Inspect attributes, logs, and errors
- Analyze request flow and timing across services and queues

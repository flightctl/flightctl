# Flight Control Prometheus Metrics

This document describes the Prometheus metrics exposed by various Flight Control components.

## Alert Exporter Metrics

The FlightCtl Alert Exporter exposes Prometheus metrics on port **8081** at the `/metrics` endpoint. 

**Note**: The metrics are exposed on the container port 8081, but there is no Kubernetes Service exposing this port externally. For external access to these metrics, you would need to either:
- Use `kubectl port-forward` to forward the port locally
- Create a Service to expose port 8081 
- Access metrics through monitoring tools configured within the cluster

### Processing Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_alert_exporter_processing_cycles_total` | Counter | Total number of processing cycles completed |
| `flightctl_alert_exporter_processing_duration_seconds` | Histogram | Time spent processing events in seconds |
| `flightctl_alert_exporter_events_processed_total` | Counter | Total number of events processed |
| `flightctl_alert_exporter_events_skipped_total` | Counter | Total number of events skipped, with `reason` label |

### Alert Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_alert_exporter_alerts_active_total` | Gauge | Current number of active alerts |
| `flightctl_alert_exporter_alerts_created_total` | Counter | Total number of alerts created |
| `flightctl_alert_exporter_alerts_resolved_total` | Counter | Total number of alerts resolved |

### Alertmanager Interaction

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_alert_exporter_alertmanager_requests_total` | Counter | Total number of requests to Alertmanager, with `status` label |
| `flightctl_alert_exporter_alertmanager_request_duration_seconds` | Histogram | Time spent sending requests to Alertmanager |
| `flightctl_alert_exporter_alertmanager_retries_total` | Counter | Total number of retries when sending to Alertmanager |

### Checkpoint Operations

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_alert_exporter_checkpoint_operations_total` | Counter | Total number of checkpoint operations, with `operation` and `status` labels |
| `flightctl_alert_exporter_checkpoint_size_bytes` | Gauge | Size of the checkpoint data in bytes |

### Health & Uptime

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_alert_exporter_uptime_seconds` | Gauge | Time since the alert exporter started in seconds |
| `flightctl_alert_exporter_last_successful_processing_timestamp` | Gauge | Unix timestamp of the last successful processing cycle |
| `flightctl_alert_exporter_errors_total` | Counter | Total number of errors encountered, with `component` and `type` labels |

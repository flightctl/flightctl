# Deploying the Observability Stack on Kubernetes/OpenShift

This guide describes how to deploy and configure the observability stack for Flight Control on Kubernetes or OpenShift, including Prometheus for metric storage and Grafana for visualization.

## Overview

Flight Control provides built-in device telemetry capabilities through the **Telemetry Gateway**, which is deployed by default with the Flight Control service. The Telemetry Gateway:

- Acts as the entry point for all device telemetry data
- Terminates mTLS connections from devices and validates device certificates
- Labels telemetry data with authenticated `device_id` and `org_id`
- Exposes metrics for scraping by Prometheus or forwards data to upstream OTLP backends

To store and visualize this telemetry data, along with Flight Control service metrics, you need to deploy an observability stack consisting of:

- **Prometheus** - Time-series database for storing metrics
- **Grafana** - Visualization and dashboarding platform

The Telemetry Gateway is automatically deployed with the Flight Control Helm chart with working default configuration, including:

- Server certificates provisioned and mounted
- CA certificate for validating device client certificates
- Device-facing OTLP gRPC listener on port 4317
- Prometheus metrics export on port 9464

## Prerequisites

- OpenShift 4.12 or later (or Kubernetes 1.25+)
- Cluster administrator access
- Flight Control already deployed via Helm chart

## Installing the Observability Stack

### Using Cluster Observability Operator (OpenShift)

1. Install the Cluster Observability Operator from OperatorHub:

   ```console
   oc create -f - <<EOF
   apiVersion: operators.coreos.com/v1alpha1
   kind: Subscription
   metadata:
     name: cluster-observability-operator
     namespace: openshift-operators
   spec:
     channel: development
     name: cluster-observability-operator
     source: redhat-operators
     sourceNamespace: openshift-marketplace
   EOF
   ```

2. Wait for the operator to be ready:

   ```console
   oc wait --for=condition=Ready pod -l app.kubernetes.io/name=cluster-observability-operator -n openshift-operators --timeout=300s
   ```

3. Create a `UIPlugin` custom resource to enable the Grafana dashboard integration:

   ```console
   oc create -f - <<EOF
   apiVersion: observability.openshift.io/v1alpha1
   kind: UIPlugin
   metadata:
     name: flightctl-observability
   spec:
     type: Grafana
     grafana:
       datasources:
         - name: FlightControl-Prometheus
           type: prometheus
           url: http://prometheus:9090
           isDefault: true
   EOF
   ```

4. Access Grafana through the OpenShift console under **Observe → Dashboards**.

### Using Prometheus Operator (Kubernetes)

For vanilla Kubernetes deployments, use the Prometheus Operator:

1. Install the Prometheus Operator:

   ```console
   kubectl apply -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/bundle.yaml
   ```

2. Create a Prometheus instance:

   ```yaml
   apiVersion: monitoring.coreos.com/v1
   kind: Prometheus
   metadata:
     name: flightctl-prometheus
     namespace: <flightctl-namespace>
   spec:
     replicas: 1
     retention: 30d
     serviceAccountName: prometheus
     serviceMonitorSelector:
       matchLabels:
         app: flightctl
     resources:
       requests:
         memory: 2Gi
         cpu: 500m
   ```

3. Install Grafana using Helm:

   ```console
   helm repo add grafana https://grafana.github.io/helm-charts
   helm install grafana grafana/grafana \
     --namespace <flightctl-namespace> \
     --set persistence.enabled=true \
     --set adminPassword=admin
   ```

### Verifying the Installation

Verify that Prometheus is scraping Flight Control metrics:

```console
# Port-forward to Prometheus
kubectl port-forward -n <flightctl-namespace> svc/prometheus 9090:9090

# In another terminal, query targets
curl http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | select(.labels.job | contains("flightctl"))'
```

## Configuring Prometheus Scraping

Configure Prometheus to scrape metrics from Flight Control components using ServiceMonitor resources.

### Scraping Flight Control API Metrics

The Flight Control API exposes service-level metrics on port 9090 at the `/metrics` endpoint.

Create a `ServiceMonitor` resource:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: flightctl-api-metrics
  namespace: <flightctl-namespace>
  labels:
    app: flightctl
spec:
  selector:
    matchLabels:
      flightctl.service: flightctl-api
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

Apply it:

```console
kubectl apply -f servicemonitor-api.yaml
```

#### Available Service Metrics

The Flight Control API exposes metrics including:

- **HTTP metrics**: Request duration, status codes, request/response sizes
- **System metrics**: CPU utilization, memory usage, disk I/O
- **Database metrics**: Connection pool stats, query durations
- **gRPC metrics**: RPC call durations and counts

For a complete reference, see [Metrics Configuration](../references/metrics.md).

### Scraping Telemetry Gateway Metrics

The Telemetry Gateway exposes device telemetry metrics on port 9464 at the `/metrics` endpoint.

Create a `ServiceMonitor` resource:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: flightctl-telemetry-gateway-metrics
  namespace: <flightctl-namespace>
  labels:
    app: flightctl
spec:
  selector:
    matchLabels:
      flightctl.service: flightctl-telemetry-gateway
  endpoints:
  - port: prometheus
    interval: 30s
    path: /metrics
```

Apply it:

```console
kubectl apply -f servicemonitor-telemetry.yaml
```

#### Understanding Device Telemetry Metrics

All device telemetry metrics include labels for filtering and aggregation:

- `device_id`: Unique device identifier
- `org_id`: Organization/tenant identifier
- Additional labels from the OpenTelemetry collector configuration

Example metrics:

- `system_cpu_utilization`: Device CPU usage percentage
- `system_memory_usage`: Device memory consumption
- `system_disk_io_bytes`: Disk I/O operations

#### Example Prometheus Queries

Get CPU usage for a specific device:

```promql
system_cpu_utilization{device_id="my-device-123"}
```

Average CPU usage across all devices in an organization:

```promql
avg(system_cpu_utilization{org_id="default"})
```

Devices with high memory usage:

```promql
system_memory_usage{state="used"} / system_memory_limit > 0.9
```

## Configuring Telemetry Gateway Forwarding (Optional)

By default, the Telemetry Gateway exports metrics for local Prometheus scraping. You can optionally configure forwarding to send telemetry data to an upstream OTLP/gRPC backend.

### When to Use Forwarding

Use forwarding when you:

- Have an existing observability infrastructure (e.g., Grafana Cloud, Datadog, New Relic)
- Want to centralize telemetry from multiple Flight Control deployments
- Need to integrate with organization-wide monitoring systems

You can configure both `export` and `forward` simultaneously. The gateway will:

- Export metrics locally for Prometheus scraping **and**
- Forward the same data to the upstream OTLP endpoint

### Basic Forwarding Configuration

Edit the Helm values file to add forwarding configuration:

```yaml
telemetryGateway:
  forward:
    endpoint: otlp.example.com:4317
    tls:
      insecureSkipTlsVerify: false
```

Create a secret with the upstream CA certificate:

```console
kubectl create secret generic telemetry-gateway-forward-ca \
  --from-file=ca.crt=/path/to/upstream-ca.crt \
  -n <flightctl-namespace>
```

Update the Helm values to mount the CA certificate:

```yaml
telemetryGateway:
  forward:
    endpoint: otlp.example.com:4317
    tls:
      insecureSkipTlsVerify: false
      caFile: /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
  extraVolumes:
    - name: forward-ca
      secret:
        secretName: telemetry-gateway-forward-ca
  extraVolumeMounts:
    - name: forward-ca
      mountPath: /etc/flightctl/flightctl-telemetry-gateway/forward
      readOnly: true
```

Then upgrade the Helm release:

```console
helm upgrade flightctl flightctl/flightctl -f values.yaml -n <flightctl-namespace>
```

### Forwarding with Mutual TLS (mTLS)

If the upstream backend requires client certificate authentication:

1. Create a secret with client certificates:

   ```console
   kubectl create secret generic telemetry-gateway-forward-certs \
     --from-file=ca.crt=/path/to/upstream-ca.crt \
     --from-file=client.crt=/path/to/client-cert.crt \
     --from-file=client.key=/path/to/client-key.key \
     -n <flightctl-namespace>
   ```

2. Update Helm values:

   ```yaml
   telemetryGateway:
     forward:
       endpoint: otlp.example.com:4317
       tls:
         insecureSkipTlsVerify: false
         caFile: /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
         certFile: /etc/flightctl/flightctl-telemetry-gateway/forward/client.crt
         keyFile: /etc/flightctl/flightctl-telemetry-gateway/forward/client.key
     extraVolumes:
       - name: forward-certs
         secret:
           secretName: telemetry-gateway-forward-certs
     extraVolumeMounts:
       - name: forward-certs
         mountPath: /etc/flightctl/flightctl-telemetry-gateway/forward
         readOnly: true
   ```

3. Upgrade the Helm release:

   ```console
   helm upgrade flightctl flightctl/flightctl -f values.yaml -n <flightctl-namespace>
   ```

### Verifying Forwarding

Check the telemetry gateway logs to verify successful forwarding:

```console
kubectl logs -f deployment/flightctl-telemetry-gateway -n <flightctl-namespace>
```

Look for log entries indicating successful OTLP exports:

```json
{"level":"info","msg":"Successfully forwarded metrics batch","endpoint":"otlp.example.com:4317","batch_size":100}
```

## Next Steps

- **Add devices**: See [Adding OpenTelemetry Collector to Devices](../building/building-images.md#optional-adding-opentelemetry-collector-to-devices) to configure devices to send telemetry
- **Create dashboards**: Import pre-built Grafana dashboards or create custom ones
- **Configure alerts**: Set up alerting rules in Prometheus and Alertmanager (see [Alerts and Monitoring](../references/alerts.md))

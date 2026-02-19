# Deploying an Observability Stack on OpenShift / Kubernetes

This guide describes how to deploy and configure an observability stack for Flight Control on OpenShift or other Kubernetes distributions.

## Overview

The Flight Control service exports three categories of metrics:

1. **Service metrics** are metrics about the Flight Control service itself such as
    - system resource utilization metrics,
    - API metrics,
    - database metrics, and
    - agent metrics.
2. **Business metrics** are metrics about the objects managed by the Flight Control service such as
    - device deployment, update, and health metrics,
    - application deployment, update, and health metrics, and
    - fleet rollout metrics.
3. **Device and application metrics** are metrics collected from the devices and their application workloads.

The first two categories can be collected from the Flight Control API server, the third from the Flight Control Telemetry Gateway. The Telemetry Gateway

- acts as the entry point for all device telemetry data,
- terminates the OpenTelemetry mTLS connections from devices and validates the client certificates,
- labels telemetry data with authenticated `device_id` and `org_id`, and
- exposes metrics for scraping by Prometheus or forwards data to upstream OTLP backends.

To store and visualize this telemetry data, you can deploy an observability stack as described in the following sections.

## Deploying the Monitoring Stack

Prerequisites:

- You have access to an OpenShift Kubernetes cluster (4.19+) with cluster admin permissions.
- You have the Flight Control service deployed via Helm chart
- You have `oc` of a matching version installed and are logged in to the OpenShift cluster.

Procedure:

1. Install the Cluster Observability Operator from the Software Catalog:

    ```console
    oc create -f - <<EOF
    apiVersion: operators.coreos.com/v1alpha1
    kind: Subscription
    metadata:
      name: cluster-observability-operator
      namespace: openshift-operators
    spec:
      channel: stable
      name: cluster-observability-operator
      source: redhat-operators
      sourceNamespace: openshift-marketplace
    EOF
    ```

2. Wait for the operator to be ready:

    ```console
    oc wait --for=jsonpath='{.status.phase}'=Succeeded \
      csv -l operators.coreos.com/cluster-observability-operator.openshift-operators \
      -n openshift-operators --timeout=300s
    ```

3. Create a `MonitoringStack` resource to deploy a Prometheus instance:

    ```console
    oc create -f - <<EOF
    apiVersion: monitoring.rhobs/v1alpha1
    kind: MonitoringStack
    metadata:
      name: flightctl-monitoring-stack
      namespace: flightctl
    spec:
      alertmanagerConfig:
        disabled: true
      logLevel: info
      prometheusConfig:
        persistentVolumeClaim:
          accessModes:
            - ReadWriteOnce
          resources:
            requests:
              storage: 2Gi
        replicas: 1
      resourceSelector:
        matchLabels:
          app: flightctl-monitoring
      retention: 365d
    EOF
    ```

4. Wait for Prometheus to be ready:

    ```console
    oc wait --for=condition=Ready pod -l app.kubernetes.io/name=prometheus \
      -n flightctl --timeout=300s
    ```

5. Configure Prometheus to scrape metrics from Flight Control components using ServiceMonitor resources:

    ```console
    oc create -f - <<EOF
    apiVersion: monitoring.rhobs/v1
    kind: ServiceMonitor
    metadata:
      name: flightctl-api
      namespace: flightctl
      labels:
        app: flightctl-monitoring
    spec:
      selector:
        matchLabels:
          flightctl.service: flightctl-api
      endpoints:
        - port: metrics
          path: /metrics
          interval: 30s
        - port: db-metrics
          path: /metrics
          interval: 30s
    ---
    apiVersion: monitoring.rhobs/v1
    kind: ServiceMonitor
    metadata:
      name: flightctl-telemetry-gateway
      namespace: flightctl
      labels:
        app: flightctl-monitoring
    spec:
      selector:
        matchLabels:
          flightctl.service: flightctl-telemetry-gateway
      endpoints:
      - port: metrics
        interval: 30s
        path: /metrics
    EOF
    ```

For a complete reference, see [Metrics Configuration](../references/metrics.md).

Verification:

1. Verify that Prometheus is successfully scraping Flight Control metrics:

    ```console
    # Port-forward to Prometheus
    oc port-forward -n flightctl svc/flightctl-monitoring-stack-prometheus 9090:9090 &

    # Query active targets
    curl -s http://localhost:9090/api/v1/targets | \
      jq -r '.data.activeTargets[] | select(.labels.job | contains("flightctl")) | {job: .labels.job, health: .health}'

    # Clean up port-forward
    pkill -f "port-forward.*prometheus"
    ```

You should see output showing healthy targets for both the API and Telemetry Gateway.

## Deploying Grafana for Visualization

Now that Prometheus is collecting metrics, deploy Grafana to visualize them.

Procedure:

1. Install the Grafana Operator:

    ```console
    oc create -f - <<EOF
    apiVersion: operators.coreos.com/v1alpha1
    kind: Subscription
    metadata:
      name: grafana-operator
      namespace: openshift-operators
    spec:
      channel: v5
      name: grafana-operator
      source: community-operators
      sourceNamespace: openshift-marketplace
    EOF
    ```

2. Wait for the operator to be ready:

    ```console
    oc wait --for=jsonpath='{.status.phase}'=Succeeded \
      csv -l operators.coreos.com/grafana-operator.openshift-operators \
      -n openshift-operators --timeout=300s
    ```

3. Create a Grafana instance:

    ```console
    oc create -f - <<EOF
    apiVersion: grafana.integreatly.org/v1beta1
    kind: Grafana
    metadata:
      name: flightctl-grafana
      namespace: flightctl
      labels:
        dashboards: flightctl
    spec:
      config:
        log:
          mode: console
        security:
          admin_user: admin
          admin_password: admin
      deployment:
        spec:
          template:
            metadata:
              labels:
                app.kubernetes.io/name: grafana
    EOF
    ```

4. Create a `NetworkPolicy` to allow the Grafana Operator to access the Grafana instance:

    ```console
    oc create -f - <<EOF
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: allow-grafana-from-openshift-operators
      namespace: flightctl
    spec:
      podSelector:
        matchLabels:
          app.kubernetes.io/name: grafana
      policyTypes:
      - Ingress
      ingress:
      - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: openshift-operators
        ports:
          - protocol: TCP
            port: 3000
    EOF
    ```

5. Create a `GrafanaDatasource` pointing to the Prometheus instance:

    ```console
    oc create -f - <<EOF
    apiVersion: grafana.integreatly.org/v1beta1
    kind: GrafanaDatasource
    metadata:
      name: flightctl-monitoring
      namespace: flightctl
    spec:
      allowCrossNamespaceImport: false
      datasource:
        access: proxy
        isDefault: true
        jsonData:
          timeInterval: 5s
          tlsSkipVerify: true
        name: prometheus
        type: prometheus
        url: 'http://flightctl-monitoring-stack-prometheus.flightctl.svc.cluster.local:9090'
      instanceSelector:
        matchLabels:
          dashboards: flightctl
    EOF
    ```

6. Create a Route to access Grafana:

    ```console
    oc create route edge flightctl-grafana-route \
      --service=flightctl-grafana-service \
      --port=grafana \
      -n flightctl
    ```

7. Get the Grafana URL:

    ```console
    echo "https://$(oc get route flightctl-grafana-route -n flightctl -o jsonpath='{.spec.host}')"
    ```

8. Access Grafana with username `admin` and password `admin`.

## Deploying Grafana Dashboards

The following procedure describes how to deploy your own Grafana dashboards, using the example Grafana dashboards in [`contrib/grafana-dashboards/`](https://github.com/flightctl/flightctl/tree/main/contrib/grafana-dashboards).

Prerequisites:

- Grafana is deployed and configured with the Prometheus data source (see previous section)

Procedure:

1. Download the dashboard JSON files from GitHub and create `ConfigMap`s:

   ```console
   curl -sLO https://raw.githubusercontent.com/flightctl/flightctl/main/contrib/grafana-dashboards/flightctl-api-dashboard.json
   curl -sLO https://raw.githubusercontent.com/flightctl/flightctl/main/contrib/grafana-dashboards/flightctl-fleet-dashboard.json

   oc create configmap flightctl-api-dashboard \
     --from-file=flightctl-api-dashboard.json \
     -n flightctl

   oc create configmap flightctl-fleet-dashboard \
     --from-file=flightctl-fleet-dashboard.json \
     -n flightctl
   ```

2. Create `GrafanaDashboard` resources to import the dashboards:

   ```console
   oc create -f - <<EOF
   apiVersion: grafana.integreatly.org/v1beta1
   kind: GrafanaDashboard
   metadata:
     name: flightctl-api-dashboard
     namespace: flightctl
   spec:
     instanceSelector:
       matchLabels:
         dashboards: flightctl
     configMapRef:
       name: flightctl-api-dashboard
       key: flightctl-api-dashboard.json
   ---
   apiVersion: grafana.integreatly.org/v1beta1
   kind: GrafanaDashboard
   metadata:
     name: flightctl-fleet-dashboard
     namespace: flightctl
   spec:
     instanceSelector:
       matchLabels:
         dashboards: flightctl
     configMapRef:
       name: flightctl-fleet-dashboard
       key: flightctl-fleet-dashboard.json
   EOF
   ```

3. Wait for the dashboards to be synchronized:

   ```console
   oc wait --for=condition=Synchronized \
     grafanadashboard/flightctl-api-dashboard \
     grafanadashboard/flightctl-fleet-dashboard \
     -n flightctl --timeout=60s
   ```

Verification:

1. Access the Grafana UI using the URL from the previous section.

2. Navigate to **Dashboards** in the left sidebar. You should see:
   - **Flight Control API Dashboard** - Shows API server metrics, database performance, and agent connectivity
   - **Flight Control Fleet Dashboard** - Shows device fleet health, rollout status, and application metrics

## Configuring Telemetry Gateway Forwarding (Optional)

By default, the Telemetry Gateway exports metrics for local Prometheus scraping. You can optionally configure forwarding to send telemetry data to an upstream OTLP/gRPC backend.

Use forwarding when you:

- Have an existing observability infrastructure (e.g., Grafana Cloud, Datadog, New Relic)
- Want to centralize telemetry from multiple Flight Control deployments
- Need to integrate with organization-wide monitoring systems

To configure forwarding with Mutual TLS (mTLS), follow the procedure below.

**Note:** The steps below show manual certificate creation for testing. For production deployments, consider using [cert-manager](https://cert-manager.io/) to automatically generate and rotate certificates. cert-manager creates `kubernetes.io/tls` secrets that work directly with this configuration.

Procedure:

1. Create TLS Secret with the client certificate and key for the Telemetry Gateway:

   ```console
   oc create secret tls telemetry-gateway-forward-certs \
     --cert=/path/to/client-cert.crt \
     --key=/path/to/client-key.key \
     -n flightctl
   ```

2. Create a ConfigMap with the upstream CA certificate:

   ```console
   oc create configmap telemetry-gateway-forward-ca \
     --from-file=ca.crt=/path/to/upstream-ca.crt \
     -n flightctl
   ```

3. Create a file called `values.yaml` with the following updated Telemetry Gateway parameters:

   ```yaml
   telemetryGateway:
     forward:
       endpoint: otlp.example.com:4317
       tls:
         insecureSkipTlsVerify: false
         caFile: /etc/flightctl/flightctl-telemetry-gateway/forward/ca/ca.crt
         certFile: /etc/flightctl/flightctl-telemetry-gateway/forward/certs/tls.crt
         keyFile: /etc/flightctl/flightctl-telemetry-gateway/forward/certs/tls.key
     extraVolumes:
       - name: forward-certs
         secret:
           secretName: telemetry-gateway-forward-certs
       - name: forward-ca
         configMap:
           name: telemetry-gateway-forward-ca
     extraVolumeMounts:
       - name: forward-certs
         mountPath: /etc/flightctl/flightctl-telemetry-gateway/forward/certs
         readOnly: true
       - name: forward-ca
         mountPath: /etc/flightctl/flightctl-telemetry-gateway/forward/ca
         readOnly: true
   ```

4. Upgrade the Helm release with the new parameters:

   ```console
   helm upgrade flightctl oci://quay.io/flightctl/charts/flightctl:${FC_VERSION} \
     -n flightctl -f values.yaml --reuse-values
   ```

   The `--reuse-values` flag preserves all existing configuration and only updates the values specified in `values.yaml`.

Verification:

1. Check the Telemetry Gateway logs to verify successful forwarding:

    ```console
    oc logs -f -n flightctl $YOUR_DEPLOYMENT/flightctl-telemetry-gateway
    ```

2. Look for log entries indicating successful OTLP exports:

    ```json
    {"level":"info","msg":"Successfully forwarded metrics batch","endpoint":"otlp.example.com:4317","batch_size":100}
    ```

## Next Steps

- **Add devices**: See [Adding OpenTelemetry Collector to Devices](../building/building-images.md#optional-adding-opentelemetry-collector-to-devices) to configure devices to send telemetry

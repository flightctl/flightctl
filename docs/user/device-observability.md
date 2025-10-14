# Device Observability

This document describes how to enable device observability in Flight Control using the **Telemetry Gateway** and a device-side **OpenTelemetry Collector**.
Devices can collect metrics from the host operating system and workloads, enabling correlation of host- and application-level metrics.
Communication between devices and the gateway is secured with **mutual TLS (mTLS)**, with device identity established through certificates issued by the Flight Control Certificate Authority (CA).

Key components:

- **Telemetry Gateway**  
  Acts as the entry point for all device telemetry. It terminates mTLS, validates device certificates against the Flight Control Certificate Authority (CA), and labels telemetry data with the authenticated `device_id` and `org_id`.

- **Device Observability Certificate**  
  Devices use the Flight Control agent to request a client certificate issued by the Flight Control CA.  
  The certificate, together with its metadata, is used for mTLS connections to the Telemetry Gateway and carries the authenticated `device_id` and `org_id`.

- **Device-side OpenTelemetry Collector**  
  Runs locally on the device to collect telemetry data (e.g., system metrics).  
  The collector uses the device certificate to establish a secure gRPC connection and send telemetry data to the Telemetry Gateway.

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
apiVersion: flightctl.io/v1alpha1
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
./bin/flightctl get csr/svc-telemetry-gateway -o yaml | python3 -c "import sys, yaml, json; print(json.dumps(yaml.safe_load(sys.stdin)))" | jq -r '.status.certificate' | base64 -d > ./certs/svc-telemetry-gateway.crt
./bin/flightctl enrollmentconfig | python3 -c "import sys, yaml, json; print(json.dumps(yaml.safe_load(sys.stdin)))" | jq -r '."enrollment-service".service."certificate-authority-data"' | base64 -d > ./certs/ca.crt
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

## Deploy OpenTelemetry Collector on the device

This shows a `bootc` based device image that installs flightctl-agent and OpenTelemetry Collector, provisions the device client certificate into the OpenTelemetry Collector paths, and configures it to send host metrics to the Telemetry Gateway over mTLS (gRPC 4317). A systemd unit is included to start the collector only after the agent has written the cert/key.

### Example: `bootc` image with OpenTelemetry Collector configured to send host metrics to the Telemetry Gateway over mTLS (gRPC)

```yaml
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y copr enable @redhat-et/flightctl && \
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
    endpoint: telemetry-gateway.192.168.1.150.nip.io:4317
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

- Replace `telemetry-gateway.192.168.1.150.nip.io:4317` with your actual gateway endpoint.
- The `flightctl.io/device-svc-client` signer requires the Common Name to include the device ID; keep common-name: `otel-{{.DEVICE_ID}}`.
- Directory `/etc/otelcol/certs` must exist and be readable by the OpenTelemetry Collector; the agent creates the cert/key with secure permissions.
- Startup ordering is handled by the unit (`After=flightctl-agent.service` + `ExecStartPre` wait loop) to avoid `file not found` on first boot.
- Ensure `/etc/otelcol/certs/ca.crt` matches the CA that signs the gateway's server certificate.

One way to extract the CA:

```bash
./bin/flightctl enrollmentconfig \
  | python3 -c "import sys, yaml, json; print(json.dumps(yaml.safe_load(sys.stdin)))" \
  | jq -r '."enrollment-service".service."certificate-authority-data"' \
  | base64 -d > /etc/otelcol/certs/ca.crt
chmod 644 /etc/otelcol/certs/ca.crt
```

### Considerations

- At the moment, certificates issued by the agent do not support changing the file owner - make sure the OpenTelemetry Collector can read them.
- Please refer to [Building Images](building-images.md) for more details on building your own OS images.

> [!TIP]  
> Instead of baking the OpenTelemetry Collector configuration into the image, you can also deliver it through the Fleet spec.  
> See [Managing OS Configuration](managing-devices.md#managing-os-configuration) and [Device Templates](managing-fleets.md#defining-device-templates).

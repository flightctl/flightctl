# OpenTelemetry Collector Certificate Configuration

This document explains how to configure the FlightCtl OpenTelemetry collector with proper TLS certificates for secure communication.

## Default Behavior

**The OpenTelemetry collector is enabled by default** in Flightctl deployments, but it will only start successfully when the required TLS certificates are provided. Without certificates, the collector pod will be in a CrashLoopBackOff state until the secrets are created and the pod is restarted.

## Quick Setup

If you want to get started quickly, you can use the automated setup script:

```bash
# Run the automated setup script
./scripts/setup-otel-collector-certs.sh
```

The script follows this exact sequence:

1. Creates CSR using `./bin/flightctl certificate request`
2. Approves the CSR automatically
3. Extracts CA certificate from enrollment config
4. Copies certificates to proper locations

Or follow the manual steps below.

## Overview

The OpenTelemetry collector requires TLS certificates to establish secure connections with clients. The collector uses mTLS (mutual TLS) authentication, which means both the server (collector) and clients need certificates signed by the same Certificate Authority (CA).

**Important**: While the collector is enabled by default, it requires TLS certificates to function. The collector pod will remain in a CrashLoopBackOff state until the required secrets are created and the pod is restarted.

## Certificate Mounting Mechanism

### How Certificates Are Mounted

The OpenTelemetry collector container accesses certificates through volume mounts:

#### **Podman/Systemd Deployment:**

- **Certificate Directory**: `/etc/otel-collector/certs/` (on host) → `/etc/otel-collector/certs/` (in container)
- **Configuration File**: `/usr/share/flightctl/flightctl-otel-collector/config.yaml` (on host) → `/root/.flightctl/config.yaml` (in container)
- **Mount Type**: Read-only volume mounts with SELinux context

#### **Kubernetes/Helm Deployment:**

- **Certificate Secrets**: Mounted as individual files from Kubernetes secrets
- **Configuration**: Generated from Helm values and mounted as ConfigMap
- **Mount Type**: Kubernetes volume mounts with proper permissions

### When Configuration Files Are Created

1. **Podman/Systemd**: Configuration files are **pre-created** during the deployment package installation
2. **Kubernetes/Helm**: Configuration files are **generated dynamically** during Helm deployment
3. **Manual**: Configuration files must be **created manually** by the user

## Manual Setup

### Certificate Requirements

The OpenTelemetry collector needs the following certificates:

1. **CA Certificate** (`ca.crt`) - The Certificate Authority certificate
2. **Server Certificate** (`server.crt`) - The collector's server certificate
3. **Server Private Key** (`server.key`) - The collector's private key

### Configuration Steps

### 1. Certificate File Locations

The OpenTelemetry collector expects certificates to be located at specific paths. These are configured in the collector's configuration:

```yaml
otelCollector:
  otlp:
    tls:
      certFile: "/etc/otel-collector/certs/server.crt"
      keyFile: "/etc/otel-collector/certs/server.key"
      clientCAFile: "/etc/otel-collector/certs/ca.crt"
```

**Note**: The script automatically places certificates in `/etc/otel-collector/certs/` with the correct filenames and permissions.

### 2. Certificate Creation

Since the OpenTelemetry collector is a service component, you need to manually create a Certificate Signing Request (CSR) using OpenSSL and flightctl.

### 3. Deployment-Specific Certificate Handling

Flightctl provides pre-configured deployments for the OpenTelemetry collector. The collector is **enabled by default** but requires certificates to work. Choose your deployment type below:

#### Using Podman/Systemd (Recommended for standalone)

For Podman/Systemd deployments using quadlets:

1. **Generate certificates** (if not already done):

   ```bash
   # Create directory for certificates
   mkdir -p ./certs
   
   # Generate ECDSA private key (PEM format - matches flightctl default)
   openssl ecparam -genkey -name prime256v1 -out ./certs/svc-otel-collector.key
   
   # Create CSR using OpenSSL with DNS names and IP addresses
   openssl req -new -key ./certs/svc-otel-collector.key \
     -subj "/CN=svc-otel-collector" \
     -addext "subjectAltName=DNS:localhost,DNS:svc-otel-collector,DNS:otel-collector,DNS:flightctl-otel-collector,IP:127.0.0.1,IP:0.0.0.0" \
     -out ./certs/svc-otel-collector.csr
   
   # Create CSR YAML file for flightctl
   cat > ./certs/csr.yaml << EOF
   apiVersion: flightctl.io/v1alpha1
   kind: CertificateSigningRequest
   metadata:
     name: svc-otel-collector
   spec:
     request: $(base64 -w 0 ./certs/svc-otel-collector.csr)
     signerName: flightctl.io/server-svc
     usages: ["clientAuth", "serverAuth"]
     expirationSeconds: 8640000
   EOF
   
   # Apply the CSR to flightctl
   ./bin/flightctl apply -f ./certs/csr.yaml
   ```

2. **Approve the CSR and extract certificates**:

   ```bash
   # Find and approve the CSR
   ./bin/flightctl get csr
   ./bin/flightctl approve csr/svc-otel-collector
   
   # Extract the issued certificate from the approved CSR
   ./bin/flightctl get csr/svc-otel-collector -o yaml | yq '.status.certificate' | base64 -d > ./certs/svc-otel-collector.crt
   
   # Extract CA certificate
   ./bin/flightctl enrollmentconfig |  yq '.enrollment-service.service.certificate-authority-data' | base64 -d > ./certs/ca.crt
   ```

3. **Copy certificates to the expected location** (quadlets mount from host):

   ```bash
   sudo mkdir -p /etc/otel-collector/certs/
   sudo cp ./certs/svc-otel-collector.crt /etc/otel-collector/certs/server.crt
   sudo cp ./certs/svc-otel-collector.key /etc/otel-collector/certs/server.key
   sudo cp ./certs/ca.crt /etc/otel-collector/certs/ca.crt
   
   # Set proper permissions
   sudo chmod 600 /etc/otel-collector/certs/server.key
   sudo chmod 644 /etc/otel-collector/certs/server.crt
   sudo chmod 644 /etc/otel-collector/certs/ca.crt
   ```

4. **Restart the OpenTelemetry collector service**:

   ```bash
   # Restart the service to pick up the new certificates
   sudo systemctl restart flightctl-otel-collector
   ```

#### Using Kubernetes/Helm

The Helm chart is configured to use user-provided certificates. You need to:

1. **Create the required secrets** with your certificates (the Helm chart expects these specific secret names):

   ```bash
   # Create the CA secret (as generic secret since we only have the CA certificate)
   kubectl create secret generic flightctl-ca-secret \
     --from-file=ca.crt=./certs/ca.crt \
     -n your-namespace

   # Create the collector certificate secret
   kubectl create secret tls otel-collector-tls \
     --cert=./certs/svc-otel-collector.crt \
     --key=./certs/svc-otel-collector.key \
     -n your-namespace
   ```

   **Note**: The Helm chart automatically mounts these secrets to the correct paths:
   - `flightctl-ca-secret` → `/etc/otel-collector/certs/ca.crt`
   - `otel-collector-tls` → `/etc/otel-collector/certs/server.crt` and `/etc/otel-collector/certs/server.key`

2. **Restart the OpenTelemetry collector pod** to pick up the new certificates:

   ```bash
   # If the pod is in CrashLoopBackOff due to missing certificates, restart it
   kubectl delete pod -l flightctl.service=flightctl-otel-collector -n flightctl-external
   
   # Or restart the deployment
   kubectl rollout restart deployment/flightctl-otel-collector -n flightctl-external
   ```

The Helm chart automatically:

- Mounts the certificates in the correct locations using the predefined secret names
- Configures the collector with the proper TLS settings
- Creates the necessary services and routes

#### Manual Container Deployment

If you prefer to run the container manually:

```bash
podman run -d \
  --name flightctl-otel-collector \
  -p 4317:4317 \
  -p 9464:9464 \
  -v /etc/otel-collector/certs:/etc/otel-collector/certs:ro \
  -v /usr/share/flightctl/flightctl-otel-collector/config.yaml:/root/.flightctl/config.yaml:ro \
  -e HOME=/root \
  quay.io/flightctl/flightctl-otel-collector:latest
```

**Note**: The manual deployment requires both the certificate directory and the configuration file to be mounted as volumes.

### 4. Configuration Files

The configuration files are already provided in the deployments:

- **Podman**: `/usr/share/flightctl/flightctl-otel-collector/config.yaml` - Pre-configured with certificate paths
- **Kubernetes**: Generated from Helm values
- **Manual**: Use the example in `examples/otel-collector-config.yaml`

**Note**: The Podman configuration file is automatically mounted into the container and expects certificates at `/etc/otel-collector/certs/`. The container definition includes a volume mount for this directory.

### 5. Prepared Configurations

Flightctl provides prepared configurations for different environments:

#### Kubernetes/Helm Configurations

- **Production**: Use `--set otelCollector.enabled=true --set otelCollector.prometheus.enabled=true` - Production-ready with proper logging
- **Development**: Use `--set otelCollector.enabled=true --set otelCollector.prometheus.enabled=true --set otelCollector.service.logLevel=debug` - Debug logging

#### Podman/Systemd Configuration

- **Enabled Target**: `deploy/podman/flightctl-otel-collector/flightctl-otel-collector-enabled.target` - Includes collector in service group

## Client Configuration

Clients connecting to the OpenTelemetry collector need to be configured with the appropriate certificates:

### Example Client Configuration

```yaml
collector:
  endpoint: "otel-collector.example.com:4317"
  mode: "mtls"  # Use mTLS for mutual authentication
  tls:
    ca_file: "/etc/client/certs/ca.crt"
  mtls:
    cert_file: "/etc/client/certs/client.crt"
    key_file: "/etc/client/certs/client.key"
    ca_file: "/etc/client/certs/ca.crt"
```

## Verification

After deployment, verify the collector is running:

```bash
# Check if the collector is responding
curl http://localhost:9464/metrics

# Check service status (Podman)
sudo systemctl status flightctl-otel-collector

# Check pod status (Kubernetes)
kubectl get pods -l flightctl.service=flightctl-otel-collector

# Verify certificate setup
./scripts/setup-otel-collector-certs.sh --verify
```

## Troubleshooting

### Common Issues

1. **Certificates not found**: Ensure secrets are created before deployment (Kubernetes) or certificates are in the correct location (Podman)
2. **Permission errors**: Check certificate file permissions (600 for keys, 644 for certs)
3. **Service won't start**: Check logs for certificate validation errors
4. **Pod in CrashLoopBackOff after creating secrets**: Restart the pod to pick up the new certificates

   ```bash
   # Restart the OpenTelemetry collector pod
   kubectl delete pod -l flightctl.service=flightctl-otel-collector -n flightctl-external
   
   # Or restart the entire deployment
   kubectl rollout restart deployment/flightctl-otel-collector -n flightctl-external
   ```

5. **Certificate Permission Errors**
   - Ensure the private key has 600 permissions
   - Ensure certificates have 644 permissions
   - Check that the container user can read the certificate files

6. **Certificate Validation Errors**
   - Verify the CA certificate is trusted by the client
   - Check that the server certificate's Common Name matches the expected service name
   - Ensure the certificate hasn't expired

7. **mTLS Authentication Failures**
   - Verify both client and server certificates are signed by the same CA
   - Check that the client certificate has the correct key usage extensions
   - Ensure the collector is configured to require client certificates

### Logs

```bash
# Podman
sudo journalctl -u flightctl-otel-collector

# Kubernetes
kubectl logs -l flightctl.service=flightctl-otel-collector
```

### Verification Commands

```bash
# Check certificate validity
openssl x509 -in /etc/otel-collector/certs/server.crt -text -noout

# Verify certificate chain
openssl verify -CAfile /etc/otel-collector/certs/ca.crt /etc/otel-collector/certs/server.crt

# Test TLS connection
openssl s_client -connect localhost:4317 -cert client.crt -key client.key -CAfile ca.crt
```

## Security Considerations

1. **Certificate Rotation**: Plan for regular certificate rotation before expiration
2. **Access Control**: Limit access to certificate files to authorized users only
3. **Network Security**: Use firewalls to restrict access to the collector ports
4. **Monitoring**: Monitor certificate expiration dates and set up alerts

## Related Documentation

- [Flightctl CLI Certificate Commands](../cli/certificate.md)
- [OpenTelemetry Collector Architecture](../architecture/otel-collector.md)
- [TLS Configuration Best Practices](../security/tls.md)

## Summary: Certificate Mounting and File Creation

### **When Configuration Files Are Created:**

1. **Podman/Systemd Deployment:**
   - **File**: `/usr/share/flightctl/flightctl-otel-collector/config.yaml`
   - **Created**: During package installation (pre-created)
   - **Mount**: Automatically mounted into container at `/root/.flightctl/config.yaml`

2. **Kubernetes/Helm Deployment:**
   - **File**: Generated from Helm values as ConfigMap
   - **Created**: During Helm deployment (dynamic)
   - **Mount**: Mounted as ConfigMap volume

3. **Manual Deployment:**
   - **File**: User must create manually
   - **Created**: When user runs the setup script or creates manually
   - **Mount**: User must specify volume mounts

### **How Certificate Mounting Works:**

#### **Podman/Systemd (Updated):**

```bash
# Container definition now includes:
Volume=/etc/otel-collector/certs:/etc/otel-collector/certs:ro,z
Volume=/usr/share/flightctl/flightctl-otel-collector/config.yaml:/root/.flightctl/config.yaml:ro,z
```

#### **Kubernetes/Helm:**

```yaml
# Certificates mounted as secrets:
- name: ca-secret
  mountPath: /etc/otel-collector/certs/ca.crt
- name: otel-collector-tls
  mountPath: /etc/otel-collector/certs/server.crt
  mountPath: /etc/otel-collector/certs/server.key
```

### **The Complete Flow:**

1. **Script runs** → Creates certificates in `/etc/otel-collector/certs/`
2. **Container starts** → Mounts certificate directory and config file
3. **Collector reads config** → Finds certificate paths in mounted config
4. **Collector loads certificates** → From mounted certificate directory
5. **mTLS authentication** → Works with proper certificates

### **Key Changes Made:**

1. **Updated Podman container definition** to mount certificate directory
2. **Updated configuration file** to use correct certificate paths
3. **Updated documentation** to explain the mounting mechanism
4. **Script now places certificates** where the container expects them

The certificate mounting now works correctly across all deployment methods!

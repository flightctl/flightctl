# Configuring ImageBuilder Worker

The Flight Control ImageBuilder Worker builds OS images for devices. This document covers configuration options for customizing the image build process.

## Prerequisites

- Flight Control deployed with ImageBuilder Worker enabled (`imageBuilderWorker.enabled: true`, which is the default)
- For RHEL subscription content: RHEL entitlement certificates

## RPM Repository Configuration

The ImageBuilder Worker installs the `flightctl-agent` package during image builds. By default, it uses the public Flight Control RPM repository.

### Default Repository

The default repository URL is:

```text
https://rpm.flightctl.io/flightctl-epel.repo
```

### Using a Custom Repository

To use a custom RPM repository (e.g., an internal mirror, air-gapped environment, or Red Hat subscription repo), configure the `rpmRepoUrl` setting.

#### Kubernetes (Helm)

```yaml
imageBuilderWorker:
  rpmRepoUrl: "https://your-internal-mirror.example.com/flightctl.repo"
```

#### Podman Quadlet

Edit `/etc/flightctl/service-config.yaml`:

```yaml
imagebuilderWorker:
  rpmRepoUrl: "https://your-internal-mirror.example.com/flightctl.repo"
```

## RHEL Entitlement Certificates

If your image builds need to access RHEL subscription-only repositories (e.g., when using RHEL base images or Red Hat packages), you need to provide entitlement certificates.

The ImageBuilder Worker automatically detects entitlement certificates mounted at `/etc/pki/entitlement` and makes them available during builds.

### Kubernetes (Helm)

#### On OpenShift

OpenShift clusters with RHEL entitlements have certificates available in the `etc-pki-entitlement` secret in the `openshift-config-managed` namespace.

##### Step 1: Copy the entitlement secret to your namespace

```bash
# Get the current namespace
NAMESPACE=$(kubectl config view --minify -o jsonpath='{..namespace}')

# Copy the secret
kubectl get secret etc-pki-entitlement -n openshift-config-managed -o yaml | \
  sed "s/namespace: openshift-config-managed/namespace: ${NAMESPACE}/" | \
  kubectl apply -f -
```

##### Step 2: Configure the Helm chart

```yaml
imageBuilderWorker:
  entitlementCertsSecretName: "etc-pki-entitlement"
```

#### On Other Kubernetes Distributions

##### Step 1: Create a secret from your entitlement certificates

```bash
# From a RHEL host with active subscription
kubectl create secret generic rhel-entitlement \
  --from-file=/etc/pki/entitlement/
```

Or create the secret from individual certificate files:

```bash
kubectl create secret generic rhel-entitlement \
  --from-file=entitlement.pem=/path/to/entitlement.pem \
  --from-file=entitlement-key.pem=/path/to/entitlement-key.pem
```

##### Step 2: Configure the Helm chart

```yaml
imageBuilderWorker:
  entitlementCertsSecretName: "rhel-entitlement"
```

### Podman Quadlet

On RHEL hosts with an active subscription, the entitlement certificates are already available at `/etc/pki/entitlement`.

#### Step 1: Enable the volume mount

Edit `/etc/flightctl/flightctl-imagebuilder-worker/flightctl-imagebuilder-worker.container` and uncomment the entitlement volume line:

```ini
[Container]
# ... other settings ...
Volume=/etc/pki/entitlement:/etc/pki/entitlement:ro,z
```

#### Step 2: Restart the service

```bash
sudo systemctl restart flightctl-imagebuilder-worker.service
```

## Configuration Reference

### Kubernetes (Helm) Values

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `imageBuilderWorker.enabled` | bool | `true` | Enable ImageBuilder Worker service |
| `imageBuilderWorker.rpmRepoUrl` | string | `""` | Custom RPM repository URL for flightctl-agent package. If empty, uses the default public repository. |
| `imageBuilderWorker.entitlementCertsSecretName` | string | `""` | Kubernetes secret containing RHEL entitlement certificates. The secret contents are mounted at `/etc/pki/entitlement`. |
| `imageBuilderWorker.logLevel` | string | `"info"` | Log level for the worker |
| `imageBuilderWorker.maxConcurrentBuilds` | int | `2` | Maximum number of concurrent image builds |
| `imageBuilderWorker.defaultTTL` | string | `"168h"` | Default time-to-live for build resources |
| `imageBuilderWorker.privileged` | bool | `true` | Run container in privileged mode (required for image builds) |

### Podman Quadlet Configuration

Edit `/etc/flightctl/service-config.yaml`:

```yaml
imagebuilderWorker:
  logLevel: info
  maxConcurrentBuilds: 2
  defaultTTL: 168h
  rpmRepoUrl: ""  # Custom RPM repository URL (optional)
```

## Troubleshooting

### Build Fails with "Package not found" Error

**Problem**: Image build fails because `flightctl-agent` package cannot be found.

**Solution**:

1. Verify the RPM repository URL is accessible from the build environment
2. Check if you need to configure a custom repository URL
3. Ensure network connectivity to the repository

### Build Fails with "Subscription required" Error

**Problem**: Image build fails when accessing RHEL subscription content.

**Solution**:

1. Verify entitlement certificates are properly configured
2. On Kubernetes: Check that the secret exists and contains valid certificates

   ```bash
   kubectl get secret <entitlement-secret-name> -o yaml
   ```

3. On Podman: Verify the host has an active RHEL subscription

   ```bash
   sudo subscription-manager status
   ```

### Entitlement Certificates Not Working

**Problem**: Entitlement certificates are configured but builds still fail.

**Diagnosis**:

1. Check ImageBuilder Worker logs:

   ```bash
   # Kubernetes
   kubectl logs deployment/flightctl-imagebuilder-worker

   # Podman
   sudo journalctl -u flightctl-imagebuilder-worker.service
   ```

2. Verify the certificate files are valid:

   ```bash
   # Check certificate expiration
   openssl x509 -in /etc/pki/entitlement/*.pem -noout -dates
   ```

**Common Causes**:

- Expired entitlement certificates
- Incorrect secret format (certificates must be PEM encoded)
- Missing certificate key file

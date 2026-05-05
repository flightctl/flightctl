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

## RHEL Configuration

When running with a RHEL subscription, you can install the `flightctl-agent` package directly from a subscription-managed repository instead of adding an external repo URL.

### Kubernetes (Helm)

The imagebuilder worker pod needs access to RHEL subscription data. This requires creating secrets from a RHEL host and mounting them into the pod.

> **Important**: The RHSM CA certificate (`/etc/rhsm/ca/redhat-uep.pem`) is required for `dnf` to verify the SSL certificate of `cdn.redhat.com`. It is provided via a dedicated `rhsmCaSecretName` secret mounted at `/etc/rhsm/ca`.

#### Preparing the yum repo configuration

Before creating the `rhel-yum-repos` secret, the `redhat.repo` file must be sanitized. The host's `/etc/yum.repos.d/redhat.repo` contains subscription-specific entitlement certificate paths (e.g., `/etc/pki/entitlement/1234567890-key.pem`) that will not match the file names once mounted inside the pod.

Replace all entitlement key/cert paths with the generic names used by the mounted secret:

```bash
# Create a working copy
cp /etc/yum.repos.d/redhat.repo /tmp/redhat.repo

# Replace host-specific entitlement cert paths with generic names
sed -i \
  -e 's|/etc/pki/entitlement/[0-9]*-key.pem|/etc/pki/entitlement/entitlement-key.pem|g' \
  -e 's|/etc/pki/entitlement/[0-9]*.pem|/etc/pki/entitlement/entitlement.pem|g' \
  /tmp/redhat.repo
```

Use this sanitized file when creating the `rhel-yum-repos` secret below.

#### On OpenShift

OpenShift clusters with RHEL entitlements have certificates available in the `etc-pki-entitlement` secret in the `openshift-config-managed` namespace.

##### Step 1: Create the required secrets

```bash
# Set the flightctl release namespace
NAMESPACE=flightctl

# Copy the entitlement secret from OpenShift
oc get secret etc-pki-entitlement -n openshift-config-managed -o yaml | \
  sed "s/namespace: openshift-config-managed/namespace: ${NAMESPACE}/" | \
  oc apply -f -

# Create secret for yum repos (using sanitized redhat.repo from the step above)
oc create secret generic rhel-yum-repos -n ${NAMESPACE} \
  --from-file=redhat.repo=/tmp/redhat.repo

# Create secret for subscription manager config
oc create secret generic rhel-rhsm -n ${NAMESPACE} \
  --from-file=/etc/rhsm/

# Create secret for RHSM CA certificates
oc create secret generic rhel-rhsm-ca -n ${NAMESPACE} \
  --from-file=/etc/rhsm/ca/
```

##### Step 2: Configure the Helm chart

```yaml
imageBuilderWorker:
  entitlementCertsSecretName: "etc-pki-entitlement"
  yumReposSecretName: "rhel-yum-repos"
  rhsmSecretName: "rhel-rhsm"
  rhsmCaSecretName: "rhel-rhsm-ca"
  rpmRepoAdd: false
  rpmRepoEnable: "edge-manager-1.0-for-rhel-9-x86_64-rpms"
```

#### On Other Kubernetes Distributions

##### Step 1: Create the required secrets from a RHEL host

```bash
# From a RHEL host with active subscription
kubectl create secret generic rhel-entitlement \
  --from-file=/etc/pki/entitlement/

# Create secret for yum repos (using sanitized redhat.repo from the preparation step above)
kubectl create secret generic rhel-yum-repos \
  --from-file=redhat.repo=/tmp/redhat.repo

# Create secret for subscription manager config
kubectl create secret generic rhel-rhsm \
  --from-file=/etc/rhsm/

# Create secret for RHSM CA certificates
kubectl create secret generic rhel-rhsm-ca \
  --from-file=/etc/rhsm/ca/
```

##### Step 2: Configure the Helm chart

```yaml
imageBuilderWorker:
  entitlementCertsSecretName: "rhel-entitlement"
  yumReposSecretName: "rhel-yum-repos"
  rhsmSecretName: "rhel-rhsm"
  rhsmCaSecretName: "rhel-rhsm-ca"
  rpmRepoAdd: false
  rpmRepoEnable: "edge-manager-1.0-for-rhel-9-x86_64-rpms"
```

- `rpmRepoAdd: false` disables the `rpmRepoAddUrl` step, which is not required since the pod has the relevant repositories mounted from the host.
- `rpmRepoEnable` tells `dnf install` to enable the specified subscription-managed repository via `--enablerepo`.

### Podman Quadlet

#### Step 1: Mount host subscription data

Edit `/usr/share/containers/systemd/flightctl-imagebuilder-worker.container` and add the following volume mounts:

```ini
[Container]
# ... other settings ...
Volume=/etc/pki/entitlement:/etc/pki/entitlement:ro,z
Volume=/etc/yum.repos.d:/etc/yum.repos.d:ro,z
Volume=/etc/rhsm/:/etc/rhsm/:ro,z
```

#### Step 2: Configure RPM repository settings

Edit `/etc/flightctl/service-config.yaml`:

```yaml
imagebuilderWorker:
  rpmRepoAdd: false
  rpmRepoEnable: "edge-manager-1.0-for-rhel-9-x86_64-rpms"
```

- `rpmRepoAdd: false` disables the `rpmRepoAddUrl` step, which is not required since the host running the flightctl services is expected to already have the relevant repositories configured.
- `rpmRepoEnable` tells `dnf install` to enable the specified subscription-managed repository via `--enablerepo`.

#### Step 3: Restart the service

```bash
sudo systemctl daemon-reload
sudo systemctl restart flightctl-imagebuilder-worker.service
```

## Configuration Reference

### Kubernetes (Helm) Values

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `imageBuilderWorker.enabled` | bool | `true` | Enable ImageBuilder Worker service |
| `imageBuilderWorker.rpmRepoUrl` | string | `""` | Custom RPM repository URL for flightctl-agent package. If empty, uses the default public repository. |
| `imageBuilderWorker.rpmRepoAdd` | bool | `true` | Whether to add the RPM repository via `dnf config-manager --add-repo`. Set to `false` for downstream/subscription-managed repos. |
| `imageBuilderWorker.rpmRepoEnable` | string | `""` | RPM repository name to enable via `--enablerepo` during `dnf install`. Only used if non-empty. |
| `imageBuilderWorker.entitlementCertsSecretName` | string | `""` | Kubernetes secret containing RHEL entitlement certificates, mounted at `/etc/pki/entitlement`. |
| `imageBuilderWorker.yumReposSecretName` | string | `""` | Kubernetes secret containing yum repository configuration files, mounted at `/etc/yum.repos.d`. |
| `imageBuilderWorker.rhsmSecretName` | string | `""` | Kubernetes secret containing RHEL subscription manager configuration, mounted at `/etc/rhsm`. |
| `imageBuilderWorker.rhsmCaSecretName` | string | `""` | Kubernetes secret containing RHSM CA certificates (e.g., `redhat-uep.pem`), mounted at `/etc/rhsm/ca`. Required for `dnf` to verify the SSL certificate of `cdn.redhat.com`. |
| `imageBuilderWorker.logLevel` | string | `"info"` | Log level for the worker |
| `imageBuilderWorker.maxConcurrentBuilds` | int | `2` | Maximum number of concurrent image builds |
| `imageBuilderWorker.defaultTTL` | string | `"168h"` | Default time-to-live for build resources |
| `imageBuilderWorker.privileged` | bool | `true` | Run container in privileged mode (required for image builds) |
| `imageBuilderWorker.serviceImages` | object | — | Builder images (podman, bootc-image-builder, Syft). Each has `image` (override image, leave empty for default) and `skipTlsVerify` (set to true to skip TLS verification when pulling that image). |
| `imageBuilderWorker.serviceImages.syft.image` | string | `""` | Syft image used for SBOM scans. If empty, the worker uses the built-in default image (see the Helm chart `README` parameter description for the current reference). |
| `imageBuilderWorker.serviceImages.syft.skipTlsVerify` | bool | `false` | Set to `true` to skip TLS verification when pulling the Syft image. |
| `imageBuilderWorker.sbom.enabled` | bool | `true` | After a successful image push, run SBOM generation (Syft) when `true`. |
| `imageBuilderWorker.sbom.pushToRegistry` | bool | `true` | Push the SBOM to the same destination registry as an OCI 1.1 referrer artifact. |
| `imageBuilderWorker.sbom.uploadToTrustify` | bool | `true` | When vulnerability reporting is enabled and Trustify is configured, upload the SBOM to Trustify. |
| `imageBuilderWorker.sbom.purlTransform` | object | — | Optional PURL normalization for CycloneDX component PURLs. Fields: `enabled`, `byType` (map of package type IDs such as `rpm` or `npm` to `namespaceMapping`, `distroMapping`, and `allowedQualifiers`). Rules apply only to PURLs with that package type (`pkg:type/...`). The worker merges your `rpm` overrides with built-in RPM defaults when you omit a field. |

### Podman Quadlet Configuration

Edit `/etc/flightctl/service-config.yaml`:

```yaml
imagebuilderWorker:
  logLevel: info
  maxConcurrentBuilds: 2
  defaultTTL: 168h
  rpmRepoUrl: ""      # Custom RPM repository URL (optional)
  rpmRepoAdd: true    # Set to false for downstream/subscription-managed repos
  rpmRepoEnable: ""   # RPM repo name for --enablerepo (optional)
  serviceImages:
    podman:
      image: ""             # Override Podman builder image (optional)
      skipTlsVerify: false  # Set to true to skip TLS verification
    bootcImageBuilder:
      image: ""             # Override bootc-image-builder image (optional)
      skipTlsVerify: false  # Set to true to skip TLS verification
    syft:
      image: ""             # Override Syft image (optional); empty uses worker default
      skipTlsVerify: false
  sbom:
    enabled: true
    pushToRegistry: true
    uploadToTrustify: true
    purlTransform:
      enabled: true
```

### Skip TLS verification

Set `imageBuilderWorker.serviceImages.podman.skipTlsVerify` and/or `imageBuilderWorker.serviceImages.bootcImageBuilder.skipTlsVerify` to `true` (Helm values or Podman `service-config.yaml`) to skip TLS verification when pulling the corresponding builder image.

## SBOM generation

After a successful image push, the ImageBuilder Worker can generate a CycloneDX JSON SBOM using Syft, optionally normalize PURLs on components, push the SBOM as an OCI 1.1 referrer on the destination registry, and upload the SBOM to Trustify when vulnerability reporting is enabled.

### Worker configuration (Helm and Podman)

Configure SBOM behavior under the ImageBuilder Worker section of the worker configuration file. With Helm, that section is rendered into the worker `ConfigMap`. With Podman quadlets, it appears in `/etc/flightctl/flightctl-imagebuilder-worker/config.yaml` after template render.

The following keys control SBOM behavior (see also the parameter table in this document and the Helm chart README for defaults):

- Syft image pull: `serviceImages.syft` (`image`, `skipTlsVerify`). Leave `image` empty to use the default Syft image pinned in the worker binary.
- SBOM switches: `sbom` (`enabled`, `pushToRegistry`, `uploadToTrustify`).
- PURL normalization: optional `sbom.purlTransform` with `enabled` and `byType`; configure `namespaceMapping`, `distroMapping`, and `allowedQualifiers` per package type (`rpm`, `npm`, and so on). The worker merges your `rpm` block with built-in RPM defaults. When the merged rules list one or more `allowedQualifiers`, only those qualifiers are retained; omit `allowedQualifiers` entirely on an extra type (for example `npm`) when you want namespace rewriting only and no qualifier stripping.

### Trustify upload and vulnerability reporting

SBOM upload to Trustify uses the same Trustify endpoint and authentication settings as the rest of Flight Control. For Helm, enable vulnerability reporting in the chart so the worker receives the Trustify block in its generated configuration. For Podman, follow the commented `vulnerabilityReporting` example in `/etc/flightctl/service-config.yaml`. For client-credentials authentication, use the same Trustify client environment variables as other Flight Control services (see [Configuring vulnerability integration](configuring-vulnerability-integration.md)).

### ImageBuild status and logs

While SBOM steps run, the build can report the `GeneratingSBOM` condition reason; the Ready condition message is `Scanning for vulnerabilities`. Worker logs include Syft invocation and push or upload steps.

### Custom CA for builder image registries

To use a custom CA for the registry that serves the Podman or bootc-image-builder image, mount the CA certificate so that podman in the worker can use it. Podman looks for registry CAs under `/etc/containers/certs.d/<registry>/ca.crt`, where `<registry>` is the registry host (and port if non-default), e.g. `my-registry.example.com` or `my-registry.example.com:5000`.

- **Helm:** Add a volume from a Secret or ConfigMap that contains the CA file (e.g. key `ca.crt`), and a volumeMount on the imagebuilder worker Deployment that mounts it at `/etc/containers/certs.d/<registry>/ca.crt`. Use the same registry host value as in your builder image reference.
- **Podman Quadlets:** Add a `Volume=` line to the imagebuilder worker container unit so the host path (or path where the CA file lives) is mounted at `/etc/containers/certs.d/<registry>/ca.crt` inside the container.

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

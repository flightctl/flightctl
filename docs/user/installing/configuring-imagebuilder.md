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
| `imageBuilderWorker.dnfTimeout` | int | `5` | DNF connection timeout in seconds for flightctl-agent installation. |
| `imageBuilderWorker.dnfRetries` | int | `0` | DNF retry count for flightctl-agent installation (0 means no retries). |
| `imageBuilderWorker.dnfSkipUnavailable` | bool | `true` | Skip packages that cannot be retrieved during flightctl-agent installation. |
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
  dnfTimeout: 5       # DNF connection timeout in seconds
  dnfRetries: 0       # DNF retry count (0 = no retries)
  dnfSkipUnavailable: true  # Skip unavailable packages
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

## Disconnected environments: base OS image

### Why ImageTagMirrorSet does not help

In a disconnected OpenShift or Kubernetes cluster you may configure an
`ImageTagMirrorSet` (or `ImageContentSourcePolicy`) to redirect pulls from
upstream registries to a local mirror. This mechanism works for container images
pulled by the cluster's CRI-O runtime, but it does **not** apply to the
imagebuilder-worker's inner podman process. The worker runs `podman build` inside
a privileged container; that podman instance has its own namespace and does not
inherit cluster-level mirror rules.

### How the base image is resolved

The Containerfile that the worker generates for every build starts with:

```dockerfile
FROM ${REGISTRY_HOSTNAME}/${IMAGE_NAME}:${IMAGE_TAG}
```

These three variables come directly from the `ImageBuild` spec:

| Containerfile arg | Source field |
|-------------------|--------------|
| `REGISTRY_HOSTNAME` | OCI registry host of the `spec.source.repository` object |
| `IMAGE_NAME` | `spec.source.imageName` |
| `IMAGE_TAG` | `spec.source.imageTag` |

Because the `FROM` value is assembled from the spec at build time, you can point
it at any registry the worker pod can reach — including a local mirror.

### Making the base image available offline

Mirror the base OS image to your internal registry on the prep machine:

```bash
skopeo copy \
  docker://quay.io/centos-bootc/centos-bootc:stream9 \
  docker://my-internal.registry.example.com:5000/centos-bootc/centos-bootc:stream9
```

Then create (or update) the `Repository` object that backs the `ImageBuild`
source so that its OCI registry points at the internal mirror:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Repository
metadata:
  name: my-bootc-source
spec:
  type: oci
  url: my-internal.registry.example.com:5000
```

Reference that repository in the `ImageBuild`:

```yaml
spec:
  source:
    repository: my-bootc-source
    imageName: centos-bootc/centos-bootc
    imageTag: stream9
```

The worker passes the mirror registry host as `REGISTRY_HOSTNAME`, so the inner
podman pulls:

```dockerfile
FROM my-internal.registry.example.com:5000/centos-bootc/centos-bootc:stream9
```

No `registries.conf` mirror configuration is required inside the worker pod.

> [!NOTE]
> If your internal registry uses a self-signed or private CA, also mount that CA
> certificate into the imagebuilder-worker pod as described in
> [Custom CA for builder image registries](#custom-ca-for-builder-image-registries).

## End-to-end disconnected image build walkthrough

This section walks through every step needed to run an image build in an air-gapped
environment. It assumes Flight Control is already deployed and an internal container
registry is reachable from both the prep machine and the cluster.

### What needs to be mirrored

| Artifact | Default upstream location | Role |
|---|---|---|
| FlightCtl imagebuilder images | `quay.io/flightctl/flightctl-imagebuilder-{api,worker}-el9` | Worker and API pods |
| podman builder image | `quay.io/podman/stable:v5.7.1` | Inner `podman build` container |
| bootc-image-builder image | `quay.io/centos-bootc/bootc-image-builder@sha256:773019f…` | Converts bootc image to disk formats |
| Syft image | `docker.io/anchore/syft:v1.44.0` | SBOM generation (disable if not needed) |
| Base OS image | e.g. `quay.io/centos-bootc/centos-bootc:stream9` | `FROM` line in the generated Containerfile |
| FlightCtl RPM repository | `https://rpm.flightctl.io` | `flightctl-agent` installed into the image |

### Step 1: Mirror images on the prep machine

Run `flightctl-mirror-images` to copy all FlightCtl service images to the internal registry:

```bash
./bin/flightctl-mirror-images --variant community-el9 \
    --dest-registry my-internal.registry.example.com:5000 \
    --execute
```

Mirror the builder service images and the base OS image with skopeo:

```bash
INTERNAL=my-internal.registry.example.com:5000

# Inner podman that runs the build
skopeo copy \
  docker://quay.io/podman/stable:v5.7.1 \
  docker://${INTERNAL}/podman/stable:v5.7.1

# bootc-image-builder (use the same digest as the binary default)
skopeo copy \
  docker://quay.io/centos-bootc/bootc-image-builder@sha256:773019f6b11766ca48170a4a7bf898be4268f3c2acfd0ec1db612408b3092a90 \
  docker://${INTERNAL}/centos-bootc/bootc-image-builder:latest

# Syft — skip if SBOM generation is disabled
skopeo copy \
  docker://docker.io/anchore/syft:v1.44.0 \
  docker://${INTERNAL}/anchore/syft:v1.44.0

# Base OS image
skopeo copy \
  docker://quay.io/centos-bootc/centos-bootc:stream9 \
  docker://${INTERNAL}/centos-bootc/centos-bootc:stream9
```

### Step 2: Set up a local RPM mirror

The imagebuilder-worker installs `flightctl-agent` into the built image using `dnf`.
The default repo URL (`https://rpm.flightctl.io`) is not reachable in a disconnected
environment. Serve a local mirror instead.

For a targeted download (individual RPMs):

```bash
sudo dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
mkdir -p ~/flightctl-rpms
dnf download --resolve --alldeps --destdir ~/flightctl-rpms flightctl-agent
createrepo_c ~/flightctl-rpms
# Serve with any HTTP server reachable from the cluster:
python3 -m http.server 8080 --directory ~/flightctl-rpms &
# Write a .repo file your cluster can fetch:
cat > ~/flightctl-local.repo <<'EOF'
[flightctl-local]
name=FlightCtl local mirror
baseurl=http://my-rpm-mirror.example.com:8080
gpgcheck=0
enabled=1
EOF
```

For a full repo mirror with proper metadata, use `dnf reposync` — see
[Setting up a local RPM repository](offline-rpm-repository.md).

### Step 3: Configure the imagebuilder-worker

Override the service images and RPM repo URL before (or alongside) deploying the
updated Helm release.

Create a `disconnected-values.yaml`:

```yaml
imageBuilderWorker:
  rpmRepoUrl: "http://my-rpm-mirror.example.com:8080/flightctl-local.repo"
  serviceImages:
    podman:
      image: "my-internal.registry.example.com:5000/podman/stable:v5.7.1"
    bootcImageBuilder:
      image: "my-internal.registry.example.com:5000/centos-bootc/bootc-image-builder:latest"
    syft:
      image: "my-internal.registry.example.com:5000/anchore/syft:v1.44.0"
```

Apply:

```bash
helm upgrade flightctl flightctl-chart.tgz \
    --reuse-values \
    -f disconnected-values.yaml
```

If the internal registry uses a private CA, also mount it into the worker pod — see
[Custom CA for builder image registries](#custom-ca-for-builder-image-registries).

### Step 4: Create Repository resources

A source `Repository` pointing at the mirrored base OS image:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Repository
metadata:
  name: bootc-source-mirror
spec:
  type: oci
  url: my-internal.registry.example.com:5000
```

A destination `Repository` for the output image:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Repository
metadata:
  name: built-images
spec:
  type: oci
  url: my-internal.registry.example.com:5000
```

Apply both:

```bash
flightctl apply -f bootc-source-mirror.yaml
flightctl apply -f built-images.yaml
```

### Step 5: Submit the ImageBuild

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageBuild
metadata:
  name: my-disconnected-build
spec:
  source:
    repository: bootc-source-mirror      # Repository resource created above
    imageName: centos-bootc/centos-bootc
    imageTag: stream9
  destination:
    repository: built-images             # Repository resource created above
    imageName: edge/my-device-image
    imageTag: v1.0.0
  binding:
    type: late
```

```bash
flightctl apply -f my-disconnected-build.yaml
```

### Step 6: Monitor the build

```bash
# Watch status transitions
flightctl get imagebuild my-disconnected-build -w

# Follow live build logs
flightctl logs imagebuild/my-disconnected-build -f
```

A successful build progresses through:
**`Pending`** → **`Building`** → **`Pushing`** → **`Completed`**

On completion, `status.imageReference` contains the full digest-pinned image
reference ready to use in a Fleet template.

### Disconnected build failure reference

| Symptom | Likely cause | Fix |
|---|---|---|
| Stuck in `Pending` | Worker pod cannot pull podman or bootc-image-builder | Set `serviceImages` overrides to internal registry |
| `FROM` pull fails | Base OS not in internal registry, or Repository URL wrong | Mirror base image; verify `spec.url` in source Repository |
| `Package not found` | RPM repo URL unreachable from build pod | Set `rpmRepoUrl` to internal mirror |
| Push fails with `no such host` | Destination registry unreachable | Verify destination Repository URL and network policy |
| TLS errors on any registry | Private CA not trusted | Mount CA into worker pod (see custom CA section) |

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

### Build Fails with "setting RLIMIT\_NOFILE limit … operation not permitted"

**Problem**: Image build fails with an error similar to:

```text
Error: building at STEP "RUN … dnf install -y … flightctl-agent …":
setting "RLIMIT_NOFILE" limit to soft=1048576,hard=1048576
(was soft=524288,hard=524288): operation not permitted
```

**Cause**: The build container (buildah) tries to raise the open-file-descriptor limit to
1048576. This fails when the host's hard limit is lower (for example, 524288, which is the
OpenShift default) and the container runtime does not grant `CAP_SYS_RESOURCE` to raise it.

**Solution**: The ImageBuilder Worker sets `--ulimit nofile=1048576:1048576` on the nested
podman worker container, relying on the privileged pod's `CAP_SYS_RESOURCE` capability.
If you still encounter this error, the host itself must be configured to allow the higher limit.

On OpenShift, apply the following `MachineConfig` to the worker nodes:

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-worker-raise-nofile
spec:
  config:
    ignition:
      version: 3.5.0
    storage:
      files:
        - contents:
            source: data:,%5BManager%5D%0ADefaultLimitNOFILE%3D1048576%3A1048576%0A
          mode: 420
          overwrite: true
          path: /etc/systemd/system.conf.d/60-nofile.conf
```

This sets `DefaultLimitNOFILE=1048576:1048576` in systemd, which raises the limit for all
processes on the node, including container runtimes.

> [!NOTE]
> `DefaultLimitNOFILE` is a system-wide default that applies to every service started by systemd
> that does not have its own `LimitNOFILE=` directive in its unit file.

On a Podman Quadlet (Linux host), use a systemd drop-in to raise the limit only for
`flightctl-imagebuilder-worker.service`, without affecting any other service on the host:

```ini
# /etc/systemd/system/flightctl-imagebuilder-worker.service.d/limits.conf
[Service]
LimitNOFILE=1048576:1048576
```

Then reload and restart the service:

```bash
sudo systemctl daemon-reload
sudo systemctl restart flightctl-imagebuilder-worker.service
```

Verify the running service has the new limit:

```bash
systemctl show flightctl-imagebuilder-worker.service | grep LimitNOFILE
```

The output should show `LimitNOFILE=1048576` and `LimitNOFILESoft=1048576`.

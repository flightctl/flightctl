# Installing Flight Control in a Disconnected OpenShift Cluster

This document describes how to install the Flight Control service on an OpenShift
cluster that has no direct internet access. A `flightctl-mirror-images` tool running on a
connected prep machine mirrors all required container images to your internal
registry. You then configure OpenShift to redirect image pulls to that registry
and install Flight Control using Helm.

For the connected (online) installation procedure, see
[Installing the Flight Control Service on OpenShift/Kubernetes](installing-service-on-kubernetes.md).

## Prerequisites

On the **prep machine** (internet-connected):

- RHEL 9 or RHEL 10 with `skopeo` installed (`sudo dnf install -y skopeo`)
- The `flightctl-mirror-images` binary built from the flightctl repository (`make build-mirror-images`)
- `helm` CLI installed
- For `rhem-el9` or `rhem-el10` variants: credentials for `registry.redhat.io`
  (`podman login registry.redhat.io`)

On the **disconnected cluster**:

- OpenShift 4.12 or later
- An internal mirror registry reachable from all cluster nodes
  (for example, Red Hat Quay, a mirror registry appliance, or Docker Registry)
- `oc` and `helm` CLI access to the cluster

## Step 1: Mirror images to the internal registry

Choose the variant that matches your deployment:

| Variant | Use when |
|---------|----------|
| `community-el9` | Community images from `quay.io` (RHEL 9 base) |
| `community-el10` | Community images from `quay.io` (RHEL 10 base) |
| `rhem-el9` | Red Hat images from `registry.redhat.io` (requires entitlement) |
| `rhem-el10` | Red Hat images from `registry.redhat.io` (requires entitlement) |

### Option A: Direct push (prep machine can reach the internal registry)

Run `flightctl-mirror-images` with `--execute` to copy all images directly to your mirror
registry in a single step:

```bash
./bin/flightctl-mirror-images \
    --variant rhem-el9 \
    --dest-registry <mirror-registry-host>:<port> \
    --execute \
    --tag-override <version>
```

Replace `<version>` with the Flight Control release version (e.g. `1.2.0`). To
find available versions:

```bash
helm show chart oci://quay.io/flightctl/charts/flightctl | grep appVersion
```

If the internal registry uses a self-signed certificate, add `--insecure`.

### Option B: Bundle then push (no direct path from prep to internal registry)

Create a portable archive on the prep machine, transfer it to a host inside the
disconnected environment, then push from there:

```bash
# On the prep machine (internet-connected)
./bin/flightctl-mirror-images \
    --variant rhem-el9 \
    --bundle ~/flightctl-bundle.tar.gz \
    --tag-override <version>

# Transfer the bundle
scp ~/flightctl-bundle.tar.gz <user>@<bastion>:~/

# On a host that can reach the internal registry
mkdir ~/flightctl-bundle
tar -xzf ~/flightctl-bundle.tar.gz -C ~/flightctl-bundle
cd ~/flightctl-bundle
./import.sh --registry <mirror-registry-host>:<port>
```

See [Packaging artifacts for portable media](offline-portable-media.md) for USB
drive and other transfer formats.

> [!IMPORTANT]
> The image tags in the bundle must match the Helm chart version you will install.
> Always pin the version with `--tag-override` or by checking out the corresponding
> git release tag (`git checkout v<version>`) before building the tool and running
> the command.

## Step 2: Download the Helm chart

Pull the Helm chart on the prep machine and transfer it to the disconnected environment:

```bash
# On the prep machine (internet-connected)
helm pull oci://quay.io/flightctl/charts/flightctl --version <version>

# Transfer to the disconnected environment
scp flightctl-<version>.tgz <user>@<bastion>:~/
```

## Step 3: Configure image mirroring on OpenShift

### What is ImageTagMirrorSet and why is it recommended?

`ImageTagMirrorSet` (ITMS) is an OpenShift cluster-level resource that tells the
container runtime (CRI-O) to redirect image pulls from one registry to another.
When a pod tries to pull `quay.io/flightctl/flightctl-api-el9:1.2.0`, CRI-O
transparently fetches it from your local mirror instead — no changes to pod specs,
Helm values, or quadlet files are required.

ITMS is the recommended approach for OpenShift air-gapped installations because:

- It operates at the cluster level — one configuration covers all namespaces and pods
- It survives Helm upgrades without re-patching image references
- It supports fallback mirrors if the primary is unavailable
- It is the official OpenShift mechanism for image content source redirection
  (replacing the deprecated `ImageContentSourcePolicy`)

> [!IMPORTANT]
> Applying an `ImageTagMirrorSet` triggers a `MachineConfig` rollout that drains
> and restarts all nodes one at a time. Plan a maintenance window for production
> clusters. On a 3-node cluster this typically takes 15–30 minutes.

### Apply the ImageTagMirrorSet for your variant

#### For `rhem-el9` or `rhem-el10`

```bash
oc apply -f - <<EOF
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
  name: flightctl-mirrors
spec:
  imageTagMirrors:
  - source: registry.redhat.io
    mirrors:
    - <mirror-registry-host>:<port>
    mirrorSourcePolicy: NeverContactSource
  - source: registry.access.redhat.com
    mirrors:
    - <mirror-registry-host>:<port>
    mirrorSourcePolicy: NeverContactSource
EOF
```

#### For `community-el9` or `community-el10`

```bash
oc apply -f - <<EOF
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
  name: flightctl-mirrors
spec:
  imageTagMirrors:
  - source: quay.io
    mirrors:
    - <mirror-registry-host>:<port>
    mirrorSourcePolicy: NeverContactSource
  - source: docker.io
    mirrors:
    - <mirror-registry-host>:<port>
    mirrorSourcePolicy: NeverContactSource
  - source: registry.access.redhat.com
    mirrors:
    - <mirror-registry-host>:<port>
    mirrorSourcePolicy: NeverContactSource
EOF
```

### Mirror policy options

The `mirrorSourcePolicy` field controls what happens when the mirror is unavailable:

| Policy | Behaviour | Use when |
|--------|-----------|----------|
| `NeverContactSource` | Only pull from mirrors; fail if no mirror works | Fully air-gapped — source registry is unreachable |
| `AllowContactingSource` | Try mirrors first, fall back to source registry | Restricted network — source is reachable but slow or rate-limited |

Use `NeverContactSource` in a fully disconnected environment to ensure no accidental
outbound connections are attempted.

### Wait for the rollout to complete

After applying the ITMS, OpenShift triggers a `MachineConfig` rollout. Wait for all
nodes to return to `Ready` state before proceeding:

```bash
oc wait nodes --all --for=condition=Ready --timeout=30m
```

Monitor rollout progress:

```bash
oc get mcp
```

All `MachineConfigPool` objects should show `UPDATED=True` and `DEGRADED=False`
before you continue.

### Verify the mirror configuration is active

Confirm that CRI-O picked up the new mirror rules on a node:

```bash
# Open a debug shell on any node
oc debug node/<node-name>

# Inside the debug shell
chroot /host
cat /etc/containers/registries.conf.d/
```

The registry configuration directory should contain a file referencing your mirror.

### Troubleshooting common ITMS issues

**Image pull still fails after ITMS applied:**

1. Verify the `MachineConfig` rollout is complete (`oc get mcp`). If nodes are still
   updating, wait for the rollout to finish.
2. Check that the image was actually mirrored to the local registry:

   ```bash
   curl https://<mirror-registry-host>:<port>/v2/<image-path>/tags/list
   ```

3. Verify the ITMS source matches the registry prefix in the image reference — ITMS
   matches on prefix, not exact registry. `quay.io` covers all `quay.io/*` images.

**Digest mismatch error (`manifest unknown` or `digest did not match`):**

The image was mirrored by tag but the pod is pulling by digest. Ensure you mirrored
the image after its final tag was pushed and that the same digest is present in the
mirror:

```bash
skopeo inspect docker://<mirror-registry-host>:<port>/<image>:<tag> | grep Digest
skopeo inspect docker://quay.io/<image>:<tag> | grep Digest
```

Both digests must match.

**`NeverContactSource` causing failures on a partially connected cluster:**

Switch to `AllowContactingSource` temporarily while diagnosing, then switch back once
the mirror is populated:

```bash
oc patch imagetag mirrorset flightctl-mirrors \
    --type=merge \
    -p '{"spec":{"imageTagMirrors":[{"source":"quay.io","mirrors":["<mirror>"],"mirrorSourcePolicy":"AllowContactingSource"}]}}'
```

## Step 4: Create an image pull secret (Red Hat variants only)

If you are using a `rhem-el9` or `rhem-el10` variant and your mirror registry
requires authentication, create a pull secret in the `flightctl` namespace:

```bash
oc create namespace flightctl --dry-run=client -o yaml | oc apply -f -
oc create secret docker-registry flightctl-pull-secret \
    --namespace flightctl \
    --docker-server=<mirror-registry-host>:<port> \
    --docker-username=<username> \
    --docker-password=<password>
```

Reference the secret in your Helm values:

```yaml
global:
  imagePullSecretName: flightctl-pull-secret
```

## Step 5: Install Flight Control via Helm

Install using the transferred chart archive and a values file that sets your
mirror registry:

```bash
helm upgrade --install \
    --namespace flightctl \
    --create-namespace \
    flightctl ./flightctl-<version>.tgz \
    --values my-values.yaml
```

A minimal `my-values.yaml` for a disconnected installation:

```yaml
global:
  baseDomain: flightctl.example.com    # FQDN of your OpenShift cluster ingress
  imagePullSecretName: flightctl-pull-secret  # omit for community variants
```

For a full list of configuration options, see
[Installing the Flight Control Service on OpenShift/Kubernetes](installing-service-on-kubernetes.md).

## Verifying the installation

Check that all pods are running:

```bash
oc get pods -n flightctl
```

Confirm the API is reachable:

```bash
curl -k https://<baseDomain>/api/v1/fleets
```

The Flight Control UI is available at the hostname set in `global.baseDomain`.
Open it in a browser to verify the service is running end-to-end.

## Optional: deploying the observability stack

The Prometheus and Grafana images are not included in the standard mirror run.
If you need the `flightctl-observability` stack, mirror those images manually
before installing the observability Helm chart.

See [Deploying an Observability Stack on Kubernetes](deploying-observability-kubernetes.md)
for image lists and configuration details.

## Next steps

1. **Set up authentication** — configure an identity provider before allowing user
   access. For OpenShift-integrated login see
   [Configuring OpenShift Authentication](configuring-auth/auth-openshift.md),
   or review the [Authentication Overview](configuring-auth/overview.md) for all options.

2. **Create an organization and assign roles** — Flight Control requires at least
   one organization before users can manage devices. See
   [Managing Organizations](configuring-auth/organizations.md) and the role
   assignment section of the authentication guide.

3. **Log in with the CLI** — see [Logging In](../using/cli/logging-in.md) for the
   `flightctl login` command and certificate trust setup.

4. **Deploy the observability stack (optional)** — see
   [Deploying an Observability Stack on Kubernetes](deploying-observability-kubernetes.md)
   for Prometheus and Grafana setup.

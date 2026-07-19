# Installing the Flight Control Service on OpenShift/Kubernetes

## System requirements

The following table shows the minimum recommended resources for the Flight Control
server based on the number of managed devices. These values were established through
benchmarking and represent the resources needed for the server workload — not the
managed edge devices themselves.

| Deployment size | Managed devices | vCPU (cores) | vRAM (GB) | vDisk (GB) |
|---|---|---|---|---|
| **Small** | 100 | 4 | 4 | 70 |
| **Medium** | 1,000 | 4 | 8 | 70 |
| **Large** | 10,000 | 4 | 16 | 70 |
| **Super Large** | 100,000 | 8 | 40 | 120 |

> [!NOTE]
> These figures are based on benchmarking with an earlier version of Flight Control
> and represent a reasonable baseline. Actual resource usage depends on polling
> intervals, fleet template complexity, and observability stack configuration. Add
> approximately 3 GB disk for each additional service component (image builder,
> observability stack).

## Installing on Kubernetes

You can install the Flight Control Service on any certified Kubernetes distribution that supports the Gateway API. If you have an OpenShift Kubernetes cluster available, refer to [Installing on OpenShift](#installing-on-openshift) for a more streamlined experience. If you are running RHEL and need a lightweight Kubernetes distribution, refer to [Installing on MicroShift](#installing-on-microshift).

It is recommended to install `cert-manager` before installing Flight Control. When the Flight Control installer detects `cert-manager`, it will use it to issue and manage required CA and server TLS certificates. Otherwise, it falls back to creating certificates using Helm's built-in functions once, but does not manage them.

### Binding to pre-provisioned PersistentVolumes

Use this section if your cluster does not support dynamic volume provisioning, or if the database or Alertmanager storage must use a specific, pre-provisioned `PersistentVolume` instead of one selected automatically by a `StorageClass`. This applies to Flight Control installations on Kubernetes, OpenShift, and MicroShift.

Prerequisites:

- The pre-provisioned `PersistentVolume` for the database is group-writable by the group ID configured in `db.builtin.fsGroup`. PostgreSQL mounts its data directory at `/var/lib/pgsql/data` and requires matching filesystem group ownership.
- The database `PersistentVolumeClaim` is not backed by NFS storage. PostgreSQL relies on file-locking and `fsync` guarantees that many NFS implementations do not provide reliably, which can lead to data corruption.
- The pre-provisioned `PersistentVolume` for the database has a capacity of at least `db.builtin.storage.size` (default `60Gi`) — Kubernetes cannot bind a claim to a volume smaller than what it requests.
- The pre-provisioned `PersistentVolume` for Alertmanager has a capacity of at least `2Gi`, the fixed size its `PersistentVolumeClaim` requests.

The Helm chart exposes a `volumeName` and a `selector.matchLabels` value for both the database and Alertmanager `PersistentVolumeClaim` resources. Set one of these values to bind that `PersistentVolumeClaim` to a matching pre-provisioned `PersistentVolume`:

```yaml
db:
  builtin:
    volumeName: "my-db-pv"       # Bind to a PersistentVolume by name
    # selector:
    #   matchLabels:
    #     disk: fast              # Or bind to a PersistentVolume by label selector

alertmanager:
  volumeName: "my-alertmanager-pv"
  # selector:
  #   matchLabels:
  #     disk: standard
```

Both values are optional and unset by default, which preserves the existing dynamic-provisioning behavior. Set only one of `volumeName` or `selector.matchLabels` per component to keep the binding unambiguous.

Setting either value on a component also makes Flight Control render `storageClassName: ""` on that `PersistentVolumeClaim` when `global.storageClassName` is not explicitly set, instead of omitting the field. This keeps the binding working regardless of whether the cluster has a default `StorageClass` — without it, Kubernetes could inject a default class that does not match the pre-provisioned `PersistentVolume` and leave the claim unbound.

> [!IMPORTANT]
> `volumeName` and `selector` take effect only when Flight Control first creates the underlying storage resource. For the database, Kubernetes does not allow changing these fields on an existing `PersistentVolumeClaim`. For Alertmanager, Kubernetes does not allow changing `volumeClaimTemplates` on an existing `StatefulSet` at all. Either way, setting or changing these values on an already-installed release causes `helm upgrade` to fail for that resource. Set the values before the first `helm install`.

If you need a binding pattern the `volumeName` and `selector.matchLabels` values do not cover, such as `matchExpressions`, or changing the binding on an already-installed release, manage the `PersistentVolumeClaim` outside the values covered here:

- For the database: pre-create the `PersistentVolumeClaim` with the exact specification you need, then add the Helm ownership label and annotations (`app.kubernetes.io/managed-by: Helm`, `meta.helm.sh/release-name`, `meta.helm.sh/release-namespace`) before running `helm install`, so Helm adopts the existing resource instead of creating a new one. See [Using Kubernetes Secrets](configuring-external-database.md#using-kubernetes-secrets) in [Configuring External PostgreSQL Database](configuring-external-database.md) for the equivalent adoption pattern applied to secrets. This does not apply to Alertmanager: its `PersistentVolumeClaim` is generated by the `StatefulSet` controller from `volumeClaimTemplates`, not rendered by Helm, so Helm ownership metadata has no effect on it.
- For Alertmanager: pre-create a `PersistentVolumeClaim` named `flightctl-alertmanager-data-flightctl-alertmanager-0` with the exact specification you need. The `StatefulSet` controller reuses an existing `PersistentVolumeClaim` that already matches its expected name instead of creating a new one from `volumeClaimTemplates`.
- Pre-bind the `PersistentVolume` to the future `PersistentVolumeClaim` (database or Alertmanager) by setting the `PersistentVolume`'s `spec.claimRef` to the claim's name and namespace before the claim exists. Kubernetes then reserves that `PersistentVolume` for the matching claim once it is created.

### Using a custom CA

If you have an existing certificate authority (CA) and want Flight Control to use it instead of generating a self-signed CA, provide a `flightctl-ca` secret in the installation namespace **before** running `helm install`.

Flight Control checks for an existing `flightctl-ca` secret during installation. If found, it will use that CA to sign all service certificates (API, UI, telemetry, imagebuilder). If not found, it creates a self-signed CA automatically.

**For cert-manager users:**

Create a Certificate resource named `flightctl-ca` with `isCA: true` before installing Flight Control. You can reference any cert-manager issuer (ClusterIssuer or Issuer):

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: flightctl-ca
  namespace: flightctl  # Must match your installation namespace
spec:
  isCA: true
  commonName: flightctl-ca
  secretName: flightctl-ca
  duration: 87600h  # 10 years
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: your-issuer-name
    kind: ClusterIssuer  # or Issuer
    group: cert-manager.io
```

**For users not using cert-manager:**

Create a Kubernetes secret named `flightctl-ca` containing your CA certificate and private key in the installation namespace:

```console
kubectl create secret tls flightctl-ca -n flightctl \
  --cert=/path/to/ca.crt \
  --key=/path/to/ca.key
```

Your CA must be capable of signing other certificates (a CA certificate, not a leaf certificate).

### (Optional) Installing a `kind` cluster

If you do not have a Kubernetes cluster available and are not running RHEL, you can use the [installation on Linux](installing-service-on-linux.md) or create a local test cluster using `kind` (Kubernetes in Docker). This section guides you through setting up a `kind` cluster with Gateway API support for use with Flight Control.

Prerequisites:

- You have Podman or Docker installed and running.
- You have `kind` version 0.31.0 or later installed. See [kind Installation](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) for details.
- You have `kubectl` installed.

**Note:** On RHEL 9 and similar systems using rootless Podman, you must configure systemd to enable `kind` to work properly. Run the following commands:

```console
sudo mkdir -p /etc/systemd/system/user@.service.d
cat << 'EOF' | sudo tee /etc/systemd/system/user@.service.d/delegate.conf
[Service]
Delegate=yes
EOF
sudo systemctl daemon-reload
```

After running these commands, log out and log back in for the changes to take effect.

Procedure:

1. Create a `kind` cluster configuration file. This configuration maps host ports to the `NodePort`s that will be used by the Envoy Gateway:

    ```console
    cat > kind-flightctl.yaml <<EOF
    kind: Cluster
    apiVersion: kind.x-k8s.io/v1alpha4
    nodes:
    - role: control-plane
      extraPortMappings:
      - containerPort: 30080
        hostPort: 8080
        protocol: TCP
      - containerPort: 30443
        hostPort: 8443
        protocol: TCP
    EOF
    ```

2. Create the `kind` cluster:

    ```console
    kind create cluster --name flightctl --config kind-flightctl.yaml
    ```

3. Verify the cluster is running:

    ```console
    kubectl cluster-info
    ```

4. Install the Gateway API CRDs:

    ```console
    kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml
    ```

5. Install the Envoy Gateway controller using Helm:

    ```console
    helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.2.5 \
      -n envoy-gateway-system --create-namespace
    ```

6. Wait for the Envoy Gateway deployment to be ready:

    ```console
    kubectl wait --timeout=5m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available
    ```

7. Create an `EnvoyProxy` resource to configure `NodePort` service type with specific ports matching the `kind` cluster configuration:

    ```console
    cat <<EOF | kubectl apply -f -
    apiVersion: gateway.envoyproxy.io/v1alpha1
    kind: EnvoyProxy
    metadata:
      name: nodeport-proxy
      namespace: envoy-gateway-system
    spec:
      provider:
        type: Kubernetes
        kubernetes:
          envoyService:
            type: NodePort
            patch:
              type: StrategicMerge
              value:
                spec:
                  ports:
                  - name: http-80
                    nodePort: 30080
                    port: 80
                    protocol: TCP
                  - name: https-443
                    nodePort: 30443
                    port: 443
                    protocol: TCP
    EOF
    ```

8. Create the Envoy Gateway `GatewayClass` with a reference to the `EnvoyProxy` resource:

    ```console
    cat <<EOF | kubectl apply -f -
    apiVersion: gateway.networking.k8s.io/v1
    kind: GatewayClass
    metadata:
      name: envoy-gateway
    spec:
      controllerName: gateway.envoyproxy.io/gatewayclass-controller
      parametersRef:
        group: gateway.envoyproxy.io
        kind: EnvoyProxy
        name: nodeport-proxy
        namespace: envoy-gateway-system
    EOF
    ```

Your `kind` cluster is now ready for Flight Control installation. Flight Control will automatically create the Gateway and routing resources when deployed.

When deploying Flight Control, use:

- `global.baseDomain=<your_ip>.nip.io` where `<your_ip>` is your host's IP address (e.g., `127.0.0.1.nip.io` for local access). The `nip.io` wildcard DNS service ensures proper resolution of subdomains like `ui.<your_ip>.nip.io` and `api.<your_ip>.nip.io`. Alternatively, use a domain you control with proper DNS configuration.
- `global.gateway.gatewayClassName=envoy-gateway` to reference the correct `GatewayClass`
- `global.gateway.ports.http=80` and `global.gateway.ports.tls=443` to match the Gateway listener ports

### Installing using the CLI

Prerequisites:

- You have access to a Kubernetes cluster (v1.35+) with cluster admin permissions.
- You have `kubectl` of a matching version installed and are logged in to the Kubernetes cluster.
- You have the [Gateway API CRDs installed](https://gateway-api.sigs.k8s.io/guides/#installing-gateway-api) on your cluster.
- You have a Gateway controller installed (e.g., Envoy Gateway or Istio).
- You have `helm` version 3.17+ installed.

Procedure:

1. Define the Flight Control version you want to install.

    ```console
    FC_VERSION=1.1.0
    ```

2. Define the namespace you want to install Flight Control into.

    ```console
    FC_NAMESPACE=flightctl
    ```

3. Define the base domain for your Flight Control installation. This should be a domain or subdomain you control. If deploying on `kind`, use the `nip.io` wildcard DNS service with your host IP for proper subdomain resolution.

    ```console
    FC_BASE_DOMAIN=127.0.0.1.nip.io
    ```

    Note: Replace `127.0.0.1` with your actual host IP if accessing from other machines. For production deployments, use `FC_BASE_DOMAIN=flightctl.example.com` with a domain you control.

4. (Optional) If you have a wildcard TLS certificate for your base domain, create a Kubernetes secret with it:

    ```console
    kubectl create secret tls flightctl-tls -n ${FC_NAMESPACE} \
      --cert=<path_to_tls_cert> \
      --key=<path_to_tls_key>
    ```

5. Deploy Flight Control by running the following command, whereby `gatewayClassName` needs to be set appropriately for your gateway, e.g. `envoy-gateway`:

    ```console
    helm upgrade --install flightctl oci://quay.io/flightctl/charts/flightctl:${FC_VERSION} \
      --namespace ${FC_NAMESPACE} --create-namespace \
      --set global.baseDomain=${FC_BASE_DOMAIN} \
      --set global.gateway.gatewayClassName=envoy-gateway \
      --set global.gateway.ports.http=80 \
      --set global.gateway.ports.tls=443
    ```

    If you created a TLS secret in step 4, add:

    ```console
      --set global.baseDomainTlsSecretName=flightctl-tls
    ```

    > [!IMPORTANT]
    > EL10 container images are not supported with Helm deployments. Use EL9 images only.

6. Wait for the pods to be in `Running` or `Completed` state:

    ```console
    kubectl get pods -n ${FC_NAMESPACE}
    ```

7. Get the external IP or hostname of your Gateway:

    ```console
    kubectl get gateway flightctl-gateway -n ${FC_NAMESPACE}
    ```

    Configure your DNS to point `*.${FC_BASE_DOMAIN}` to this address.

You can then access the Flight Control UI by navigating your web browser to:

```console
https://ui.${FC_BASE_DOMAIN}
```

Or for HTTP if you did not configure TLS:

```console
http://ui.${FC_BASE_DOMAIN}
```

To use the `flightctl` CLI with your service:

1. Get the `ServiceAccount` token for the default admin user:

    ```console
    FC_TOKEN=$(kubectl create token flightctl-admin -n ${FC_NAMESPACE} --duration=24h)
    ```

2. Log in to your Flight Control service:

    ```console
    FC_API_ENDPOINT=api.${FC_BASE_DOMAIN}

    flightctl login ${FC_API_ENDPOINT} -t ${FC_TOKEN}
    ```

## Installing on OpenShift

When you install the Flight Control Service on the OpenShift Kubernetes distribution, the installer automatically leverages OpenShift's built-in features like an OAuth2 authentication server and multi-tenancy support.

It is recommended to install the `cert-manager` Operator from the OpenShift Software Catalog before installing Flight Control. When the Flight Control installer detects `cert-manager`, it will use it to issue and manage required CA and server TLS certificates. Otherwise, it falls back to creating certificates using `openssl` once, but does not manage them.

To use a custom CA instead of Flight Control's self-signed CA, see [Using a custom CA](#using-a-custom-ca) in the Kubernetes installation section above.

To bind the database or Alertmanager storage to a pre-provisioned `PersistentVolume`, see [Binding to pre-provisioned PersistentVolumes](#binding-to-pre-provisioned-persistentvolumes) in the Kubernetes installation section above.

### Installing using the CLI

Prerequisites:

- You have access to an OpenShift Kubernetes cluster (4.19+) with cluster admin permissions.
- You have `oc` of a matching version installed and are logged in to the OpenShift cluster.
- You have `helm` version 3.17+ installed.

Procedure:

1. Define the Flight Control version you want to install.

    ```console
    FC_VERSION=1.1.0
    ```

2. Define the namespace you want to install Flight Control into.

    ```console
    FC_NAMESPACE=flightctl
    ```

3. Deploy that version by running:

    ```console
    helm upgrade --install flightctl oci://quay.io/flightctl/charts/flightctl:${FC_VERSION} \
      --namespace ${FC_NAMESPACE} --create-namespace
    ```

    > [!IMPORTANT]
    > EL10 container images are not supported with Helm deployments. Use EL9 images only.

4. Wait for the pods to be in `Running` or `Completed` state:

    ```console
    oc get pods -n ${FC_NAMESPACE}
    ```

You can then access the Flight Control UI by navigating your web browser to the URL returned by this command:

```console
oc get route flightctl-ui -n ${FC_NAMESPACE} -o jsonpath='{.spec.host}'
```

Alternatively, you can use the following commands to log the `flightctl` CLI in to your service:

```console
FC_API_ENDPOINT=$(oc get route flightctl-api -n ${FC_NAMESPACE} -o jsonpath='{.spec.host}')

flightctl login ${FC_API_ENDPOINT} -t $(oc whoami -t)
```

### Installing from the OpenShift Software Catalog

Prerequisites:

- You have access to an OpenShift Kubernetes cluster (4.19+) with cluster admin permissions.

Procedure:

1. On the OpenShift console with the **Administrator** view selected, navigate to **Home > Projects**, click the blue "Create Project" button, in the field "Name" enter "flightctl", and click the blue "Create" button.

2. Navigate to **Ecosystem > Software Catalog**. In the search bar below "All Items", enter "flightcontrol", click the "Flight Control" tile, and click the blue "Create" button.

3. Verify the selected project is "flightctl". Optionally, select a Flight Control version to install. Then click the blue "Create" button.

4. Navigate to **Workloads > Pods** and wait for the pods to be in `Running` or `Completed` state.

5. Navigate to **Networking > Routes** and click on the `flightctl-ui` route.

6. Click on the URL shown under "Location" to access the Flight Control UI with your current user account and credentials.

If you need to access your service using the `flightctl` CLI, find and click the "Copy login command" button to see the command to use to log the `flightctl` CLI in to your service.

## Installing on MicroShift

MicroShift is the recommended lightweight Kubernetes distribution for RHEL. It is installed as an RPM, runs as a systemd service, and exposes OpenShift-compatible APIs including Routes and OAuth. The Flight Control Helm chart automatically detects MicroShift as an OpenShift cluster and uses Routes for service exposure, so no Gateway API configuration is required.

To bind the database or Alertmanager storage to a pre-provisioned `PersistentVolume`, see [Binding to pre-provisioned PersistentVolumes](#binding-to-pre-provisioned-persistentvolumes) in the Kubernetes installation section above.

Prerequisites:

- You have a RHEL 9 host with an active Red Hat subscription.
- You have a Red Hat pull secret downloaded from [console.redhat.com](https://console.redhat.com) → OpenShift → Downloads.
- You have `helm` version 3.17+ installed.

### Step 1: Install MicroShift

1. Enable the MicroShift and Fast Datapath repositories:

    ```console
    sudo subscription-manager repos \
        --enable rhocp-4.17-for-rhel-9-x86_64-rpms \
        --enable fast-datapath-for-rhel-9-x86_64-rpms
    ```

2. Install MicroShift:

    ```console
    sudo dnf install -y microshift
    ```

3. Copy your pull secret to the required location:

    ```console
    sudo cp ~/openshift-pull-secret.json /etc/crio/openshift-pull-secret
    sudo chmod 600 /etc/crio/openshift-pull-secret
    ```

4. Enable and start MicroShift:

    ```console
    sudo systemctl enable --now microshift
    ```

### Step 2: Configure kubeconfig access

```console
mkdir -p ~/.kube
sudo cat /var/lib/microshift/resources/kubeadmin/$(hostname)/kubeconfig > ~/.kube/config
```

Verify the cluster is running:

```console
kubectl get nodes
```

### Step 3: Install Flight Control

1. Define the Flight Control version and namespace:

    ```console
    FC_VERSION=1.1.0
    FC_NAMESPACE=flightctl
    ```

2. Deploy Flight Control:

    ```console
    helm upgrade --install flightctl oci://quay.io/flightctl/charts/flightctl:${FC_VERSION} \
      --namespace ${FC_NAMESPACE} --create-namespace
    ```

    > [!IMPORTANT]
    > EL10 container images are not supported with Helm deployments. Use EL9 images only.

3. Wait for the pods to be in `Running` or `Completed` state:

    ```console
    kubectl get pods -n ${FC_NAMESPACE} -w
    ```

### Step 4: Access the service

Get the UI URL:

```console
oc get route flightctl-ui -n ${FC_NAMESPACE} -o jsonpath='{.spec.host}'
```

Log in with the `flightctl` CLI:

```console
FC_API_ENDPOINT=$(oc get route flightctl-api -n ${FC_NAMESPACE} -o jsonpath='{.spec.host}')
FC_TOKEN=$(kubectl create token flightctl-admin -n ${FC_NAMESPACE} --duration=24h)

flightctl login ${FC_API_ENDPOINT} -t ${FC_TOKEN}
```

## Installing with Advanced Cluster Management

## Installing with Ansible Automation Platform

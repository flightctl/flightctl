# Installing the Flight Control Service on OpenShift/Kubernetes

## Installing on Kubernetes

You can install the Flight Control Service on any certified Kubernetes distribution that supports the Gateway API. If you have an OpenShift Kubernetes cluster available, refer to [Installing on OpenShift](#installing-on-openshift) for a more streamlined experience.

It is recommended to install `cert-manager` before installing Flight Control. When the Flight Control installer detects `cert-manager`, it will use it to issue and manage required CA and server TLS certificates. Otherwise, it falls back to creating certificates using Helm's built-in functions once, but does not manage them.

### (Optional) Installing a `kind` cluster

If you do not have a Kubernetes cluster available, you can use the [installation on Linux](installing-service-on-linux.md) or create a local test cluster using `kind` (Kubernetes in Docker). This section guides you through setting up a `kind` cluster with Gateway API support for use with Flight Control.

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

## Installing with Advanced Cluster Management

Flight Control can be deployed alongside Advanced Cluster Management (ACM) for enhanced multi-cluster management capabilities.

### Prerequisites

- Advanced Cluster Management installed and configured
- Access to an OpenShift cluster with ACM
- Cluster admin permissions

### Flight Control in ACM

To install a released version of the Flight Control Service into the cluster, first ensure you have a `values.acm.yaml` file.

If you are not running helm from the base directory of this repository, you can find it at `deploy/helm/flightctl/values.acm.yaml`, otherwise you will need to create it.

Then run the following command, making sure to specify the correct path to `values.acm.yaml`:

```console
helm upgrade --install --version=<version-to-install> \
    --namespace flightctl --create-namespace \
    flightctl oci://quay.io/flightctl/charts/flightctl \
    --values deploy/helm/flightctl/values.acm.yaml

```

Optionally, you may change the deployed UI version adding `--set ui.image.tag=<ui-version-to-install>`.
Available versions can be found in [quay.io](https://quay.io/repository/flightctl/flightctl-ocp-ui?tab=tags)
Verify your Flight Control Service is up and running:

```console
kubectl get pods -n flightctl

```

After deploying the Flight Control ACM UI plugin, it needs to be manually enabled. Open your OpenShift Console -> Home -> Overview -> Status card -> Dynamic plugins and enable the Flight Control ACM UI plugin.
After enabling the plugin, you will need to wait for the Console operator to rollout a new deployment.

## Container OS Compatibility

Flight Control supports container images built for different CentOS Stream versions to ensure compatibility across diverse environments:

- **EL9 (Enterprise Linux 9)**: Default and recommended for most deployments
- **EL10 (Enterprise Linux 10)**: For environments requiring latest OS version

### Selecting Container Flavors

By default, Helm charts use EL9 container images. For specific OS compatibility requirements, you can configure the deployment to use EL10 images:

```yaml
# values.yaml
global:
  image:
    tag: "el10-latest"  # Use EL10 containers instead of default EL9

# Or specify individual component tags
api:
  image:
    tag: "el10-latest"
worker:
  image:
    tag: "el10-latest"
```

### Cross-Version Compatibility

The FLAVOR system enables testing and deployment scenarios like:

- **RHEL 9 control plane + RHEL 10 agents**: Forward compatibility testing
- **RHEL 10 control plane + RHEL 9 agents**: Backward compatibility support

This ensures smooth migrations and mixed-environment deployments.

## Gathering deployment information

After the helm install command succeeded, it will print out a block of helpful information.
It will look similar to:

```console
Thank you for installing Flight Control.


You can access the Flight Control UI at <UI_URL>
You can access the Flight Control API at <API_URL>

You can login using the following CLI command:
   
    flightctl login <API_URL> --insecure-skip-tls-verify --web

```

Lets store the API_URL as environment variable for later use.

```console
FC_API_URL=<API_URL>

```

For managing ongoing Flight Control deployments including upgrades and monitoring, refer to the [Helm Chart Documentation](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl/README.md).

## Installing the Flight Control CLI

In a terminal, select the appropriate Flight Control CLI binary for your OS (linux or darwin) and CPU architecture (amd64 or arm64), for example:

```console
FC_CLI_BINARY=flightctl-linux-amd64

```

Download the `flightctl` binary to your machine:

```console
$ curl -LO https://github.com/flightctl/flightctl/releases/latest/download/${FC_CLI_BINARY}

  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
  0     0    0     0    0     0      0      0 --:--:-- --:--:-- --:--:--     0
100 29.9M  100 29.9M    0     0  5965k      0  0:00:05  0:00:05 --:--:-- 7341k
```

Verify the downloaded binary has the correct checksum:

```console
$ echo "$(curl -L -s https://github.com/flightctl/flightctl/releases/download/latest/${FC_CLI_BINARY}-sha256.txt)  ${FC_CLI_BINARY}" | shasum --check

flightctl-linux-amd64: OK
```

If the checksum is correct, rename it to `flightctl` and make it executable:

```console
mv "${FC_CLI_BINARY}" flightctl && chmod +x flightctl

```

Finally, move it into a location within your shell's search path.

## Logging into the Flight Control Service from the CLI

### Standalone deployment

Retrieve the password for the "demouser" account that's been automatically generated for you during installation:

```console
FC_PASS=$(kubectl get secret/keycloak-demouser-secret -n $FC_NAMESPACE -o=jsonpath='{.data.password}' | base64 -d)

```

Headless login:

```console
flightctl login ${FC_API_URL} -u demouser -p ${FC_PASS}

```

> 📌 For headless login to work, the OIDC provider of your choice needs to have Direct Access grant enabled

Login using web browser:

```console
flightctl login ${FC_API_URL} --web

```

### ACM deployment

Login using a user token:

```console
flightctl login ${FC_API_URL} -t $(oc whoami -t)

```

> 📌 You can also login with your ACM login credentials using the `--web` or `--username` and `--password` flags

### Certificate Configuration and Troubleshooting

The CLI uses the host's certificate authority (CA) pool to verify the Flight Control service's identity. If certificate verification fails, the CLI prints the underlying error message. The tips below cover common cases and may not apply to every environment; if they don’t help, verify the API URL and certificate chain or contact your administrator.

#### Common Certificate Issues

#### Certificate Not Trusted

If you see "Cause: certificate not trusted", the server uses a custom certificate authority:

```console
flightctl login ${FC_API_URL} --certificate-authority=/path/to/ca.crt
```

#### Self-Signed Certificates

For self-signed certificates, you can either:

```console
flightctl login ${FC_API_URL} --insecure-skip-tls-verify
```

or provide the certificate as a CA:

```console
flightctl login ${FC_API_URL} --certificate-authority=<path_to_ca_crt>
```

#### Hostname Mismatch

If the certificate hostname doesn't match the URL, verify you're using the correct API endpoint. The error message will show valid hostnames for the certificate.

#### OAuth Certificate Issues

When OAuth endpoints use different certificates than the main API, use the dedicated OAuth CA flag:

```console
flightctl login ${FC_API_URL} --auth-certificate-authority=<path_to_oauth-ca_crt>
```

#### Certificate Flags Reference

| Flag | Purpose |
|------|---------|
| `--certificate-authority=<path>` | Specify CA certificate for API endpoints |
| `--auth-certificate-authority=<path>` | Specify CA certificate for OAuth endpoints |
| `--insecure-skip-tls-verify` | Skip all certificate verification |

## Building a Bootable Container Image including the Flight Control Agent

Next, we will use [Podman](https://github.com/containers/podman) to build a [bootable container image (bootc)](https://bootc-dev.github.io/bootc/) that includes the Flight Control Agent binary and configuration. The configuration contains the connection details and credentials required by the agent to discover the service and send an enrollment request to the service.

Retrieve the agent configuration with enrollment credentials by running:

```console
flightctl certificate request --signer=enrollment --expiration=365d --output=embedded > agentconfig.yaml
```

The returned `agentconfig.yaml` should look similar to this:

```console
$ cat agentconfig.yaml
enrollment-service:
  service:
    server: https://agent-api.flightctl.127.0.0.1.nip.io:7443
    certificate-authority-data: LS0tLS1CRUdJTiBD...
  authentication:
    client-certificate-data: LS0tLS1CRUdJTiBD...
    client-key-data: LS0tLS1CRUdJTiBF...
  enrollment-ui-endpoint: https://ui.flightctl.127.0.0.1.nip.io:8081
```

Create a `Containerfile` with the following content:

```console
$ cat Containerfile

FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent; \
    dnf -y clean all; \
    systemctl enable flightctl-agent.service

# Optional: to enable podman-compose application support uncomment below”
# RUN dnf -y install epel-release epel-next-release && \
#    dnf -y install podman-compose && \
#    systemctl enable podman.service

ADD agentconfig.yaml /etc/flightctl/config.yaml
```

Note this is a regular `Containerfile` that you're used to from Docker/Podman, with the difference that the base image referenced in the `FROM` directive is bootable. This means you can use standard container build tools and workflows.

For example, as a user of Quay who has the privileges to push images into the `quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl` repository, build the bootc image like this:

```console
sudo podman build -t quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1 .

```

Log in to your Quay account:

```console
$ sudo podman login quay.io

Username: ******
Password: ******
Login Succeeded!
```

Push your bootc image to Quay:

```console
sudo podman push quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1

```

## Provisioning a Device with a Bootable Container Image

A bootc image is a file system image, i.e. it contains the files to be written into an existing file system, but not the disk layout and the file system itself. To provision a device, you need to generate a full disk image containing the bootc image.

Use the [`bootc-image-builder`](https://github.com/osbuild/bootc-image-builder) tool to generate that disk image as follows:

```console
mkdir -p output && \
  sudo podman run --rm -it --privileged --pull=newer --security-opt label=type:unconfined_t \
    -v ${PWD}/output:/output -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type raw quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1
```

Once `bootc-image-builder` completes, you'll find the raw disk image under `output/image/disk.raw`. Now you can flash this image to a device using standard tools like [arm-image-installer](https://docs.fedoraproject.org/en-US/iot/physical-device-setup/#_scripted_image_transfer_with_arm_image_installer), [Etcher](https://etcher.balena.io/), or [`dd`](https://docs.fedoraproject.org/en-US/iot/physical-device-setup/#_manual_image_transfer_with_dd).

For other image types like QCoW2 or VMDK or ways to install via USB stick or network, see [Building Images](../building/building-images.md).

## Enrolling a Device

When the Flight Control Agent first starts, it sends an enrollment request to the Flight Control Service. You can see the list of requests pending approval with:

```console
$ flightctl get enrollmentrequests

NAME                                                  APPROVAL  APPROVER  APPROVED LABELS
54shovu028bvj6stkovjcvovjgo0r48618khdd5huhdjfn6raskg  Pending   <none>    <none>    
```

You can approve an enrollment request and optionally add labels to the device:

```console
$ flightctl approve -l region=eu-west-1 -l site=factory-berlin er/54shovu028bvj6stkovjcvovjgo0r48618khdd5huhdjfn6raskg

Success.

$ flightctl get enrollmentrequests

NAME                                                  APPROVAL  APPROVER  APPROVED LABELS
54shovu028bvj6stkovjcvovjgo0r48618khdd5huhdjfn6raskg  Approved  demouser  region=eu-west-1,site=factory-berlin
```

After the enrollment completes, you can find the device in the list of devices:

```console
$ flightctl get devices

NAME                                                  OWNER   SYSTEM  UPDATED     APPLICATIONS
54shovu028bvj6stkovjcvovjgo0r48618khdd5huhdjfn6raskg  <none>  Online  Up-to-date  <none>
```

## Where to go from here

Now that you have a Flight Control-managed device, refer to [Managing Devices](../using/managing-devices.md) and [Managing Fleets](../using/managing-fleets.md) for how to configure and update individual or fleets of devices.

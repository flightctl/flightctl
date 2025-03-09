# Getting Started

The following is an opinionated way of getting started with Flight Control on a local Kind cluster. Please refer to [Installing the Flight Control Service](installing-service.md) and [Installing the Flight Control CLI](installing-cli.md) for the full documentation including other installation options.

## Deploying a Local Kind Cluster

Install the following prerequisites by following the respective documentation:

* `kind` latest version ([installation guide](https://kind.sigs.k8s.io/docs/user/quick-start/))
* `kubectl` CLI of a matching version ([installation guide](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/))
* `helm` CLI version v3.15 or later ([installation guide](https://helm.sh/docs/intro/install/))

Deploy a Kind cluster:

```console
$ kind create cluster

enabling experimental podman provider
Creating cluster "kind" ...
[...]
```

Verify the cluster is up and you can access it:

```console
$ kubectl get pods -A

NAMESPACE            NAME                                         READY   STATUS    RESTARTS   AGE
kube-system          coredns-76f75df574-v6plv                     1/1     Running   0          49s
kube-system          coredns-76f75df574-xfm2w                     1/1     Running   0          49s
kube-system          etcd-kind-control-plane                      1/1     Running   0          61s
kube-system          kindnet-kznkx                                1/1     Running   0          49s
kube-system          kube-apiserver-kind-control-plane            1/1     Running   0          61s
kube-system          kube-controller-manager-kind-control-plane   1/1     Running   0          61s
kube-system          kube-proxy-qffqj                             1/1     Running   0          49s
kube-system          kube-scheduler-kind-control-plane            1/1     Running   0          65s
local-path-storage   local-path-provisioner-7577fdbbfb-wxbck      1/1     Running   0          49s
```

Verify Helm is installed and can access the cluster:

```console
$ helm list

NAME  NAMESPACE  REVISION  UPDATED  STATUS  CHART  APP VERSION
```

## Deploying the Flight Control Service

### Standalone Flight Control on k8s/KIND

Start your k8s/KIND cluster. For KIND cluster you can use [example config](../../deploy/kind.yaml).

```console
$ kind create cluster --config kind.yaml

[...]
```

Install a released version of the Flight Control Service into the cluster by running:

```console
$ helm upgrade --install --version=<version-to-install> \
    --namespace flightctl --create-namespace \
    flightctl oci://quay.io/flightctl/charts/flightctl \
    --set global.baseDomain=${YOUR_IP}.nip.io \
    --set global.exposeServicesMethod=nodePort

```

Optionally, you may change the deployed UI version adding `--set ui.image.tag=<ui-version-to-install>`.
Available versions can be found in [quay.io](https://quay.io/repository/flightctl/flightctl-ui?tab=tags)

### Flight Control on OpenShift

#### Standalone Flight Control with built-in Keycloak

Install a released version of the Flight Control Service into the cluster by running:

```console
$ helm upgrade --install --version=<version-to-install> \
    --namespace flightctl --create-namespace \
    flightctl oci://quay.io/flightctl/charts/flightctl

```

Verify your Flight Control Service is up and running:

```console
$ kubectl get pods -n flightctl

[...]
```

#### Standalone Flight Control with external OIDC

Create a values.yaml file with the following content

```yaml
global:
  auth:
    type: oidc
    oidc:
      oidcAuthority: https://oidc/realms/your_realm 
      externalOidcAuthority: https://external.oidc/realms/your_realm

```

Install a released version of the Flight Control Service into the cluster by running:

```console
$ helm upgrade --install --version=<version-to-install> \
    --namespace flightctl --create-namespace \
    flightctl oci://quay.io/flightctl/charts/flightctl \
    --values values.yaml

```

Verify your Flight Control Service is up and running:

```console
$ kubectl get pods -n flightctl

[...]
```

#### Flight Control in ACM

To install a released version of the Flight Control Service into the cluster, first ensure you have a `values.acm.yaml` file.

If you are not running helm from the base directory of this repository, you can find it at `deploy/helm/flightctl/values.acm.yaml`, otherwise you will need to create it.

Then run the following command, making sure to specify the correct path to `values.acm.yaml`:

```console
$ helm upgrade --install --version=<version-to-install> \
    --namespace flightctl --create-namespace \
    flightctl oci://quay.io/flightctl/charts/flightctl \
    --values deploy/helm/flightctl/values.acm.yaml

```

Optionally, you may change the deployed UI version adding `--set ui.image.tag=<ui-version-to-install>`.
Available versions can be found in [quay.io](https://quay.io/repository/flightctl/flightctl-ocp-ui?tab=tags)
Verify your Flight Control Service is up and running:

```console
$ kubectl get pods -n flightctl

[...]
```

After deploying the Flight Control ACM UI plugin, it needs to be manually enabled. Open your OpenShift Console -> Home -> Overview -> Status card -> Dynamic plugins and enable the Flight Control ACM UI plugin.
After enabling the plugin, you will need to wait for the Console operator to rollout a new deployment.

## Installing the Flight Control CLI

In a terminal, select the appropriate Flight Control CLI binary for your OS (linux or darwin) and CPU architecture (amd64 or arm64), for example:

```console
$ FC_CLI_BINARY=flightctl-linux-amd64

[...]
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
$ mv "${FC_CLI_BINARY}" flightctl && chmod +x flightctl

[...]
```

Finally, move it into a location within your shell's search path.

## Logging into the Flight Control Service from the CLI

### Standalone deployment

Retrieve the password for the "demouser" account that's been automatically generated for you during installation:

```console
$ kubectl get secret/keycloak-demouser-secret -n flightctl -o=jsonpath='{.data.password}' | base64 -d

[...]
```

Use the CLI to log into the Flight Control Service:

```console
$ flightctl login https://api.flightctl.127.0.0.1.nip.io/ --web --insecure-skip-tls-verify

[...]
```

In the web browser that opens, use the login "demouser" and the password you retrieved in the previous step.

Verify you can now access the service via the CLI:

```console
$ flightctl get devices

NAME                                                  OWNER   SYSTEM  UPDATED     APPLICATIONS  LAST SEEN
```

### ACM deployment

Use the CLI to log into the Flight Control Service:

```console
$ flightctl login https://api.flightctl.127.0.0.1.nip.io/ --web --insecure-skip-tls-verify

[...]
```

In the web browser that opens, use your ACM login credentials.

Verify you can now access the service via the CLI:

```console
$ flightctl get devices

NAME                                                  OWNER   SYSTEM  UPDATED     APPLICATIONS  LAST SEEN
```

## Login into the Flight Control Service from the standalone UI

Browse to `ui.flightctl.MY.DOMAIN` and use the login "demouser" and the password you retrieved in the previous step.

## Building a Bootable Container Image including the Flight Control Agent

Next, we will use [Podman](https://github.com/containers/podman) to build a [bootable container image (bootc)](https://containers.github.io/bootc/) that includes the Flight Control Agent binary and configuration. The configuration contains the connection details and credentials required by the agent to discover the service and send an enrollment request to the service.

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

RUN dnf -y copr enable @redhat-et/flightctl && \
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
$ sudo podman build -t quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1 .

[...]
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
$ sudo podman push quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1

[...]
```

## Provisioning a Device with a Bootable Container Image

A bootc image is a file system image, i.e. it contains the files to be written into an existing file system, but not the disk layout and the file system itself. To provision a device, you need to generate a full disk image containing the bootc image.

Use the [`bootc-image-builder`](https://github.com/osbuild/bootc-image-builder) tool to generate that disk image as follows:

```console
$ mkdir -p output && \
  sudo podman run --rm -it --privileged --pull=newer --security-opt label=type:unconfined_t \
    -v $(pwd)/output:/output -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type raw quay.io/${YOUR_QUAY_ORG}/centos-bootc-flightctl:v1

[...]
```

Once `bootc-image-builder` completes, you'll find the raw disk image under `output/image/disk.raw`. Now you can flash this image to a device using standard tools like [arm-image-installer](https://docs.fedoraproject.org/en-US/iot/physical-device-setup/#_scripted_image_transfer_with_arm_image_installer), [Etcher](https://etcher.balena.io/), or [`dd`](https://docs.fedoraproject.org/en-US/iot/physical-device-setup/#_manual_image_transfer_with_dd).

For other image types like QCoW2 or VMDK or ways to install via USB stick or network, see [Building Images](building-images.md).

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

NAME                                                  OWNER   SYSTEM  UPDATED     APPLICATIONS  LAST SEEN
54shovu028bvj6stkovjcvovjgo0r48618khdd5huhdjfn6raskg  <none>  Online  Up-to-date  <none>        3 seconds ago
```

## Where to go from here

Now that you have a Flight Control-managed device, refer to [Managing Devices](managing-devices.md) and [Managing Fleets](managing-fleets.md) for how to configure and update individual or fleets of devices.

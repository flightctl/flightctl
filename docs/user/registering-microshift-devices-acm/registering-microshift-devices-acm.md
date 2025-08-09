# Auto-Registering Devices with MicroShift into ACM

If you have fleets of devices running an OS image that includes MicroShift, you can configure these fleets to auto-register MicroShift clusters with Red Hat Advanced Cluster Management (ACM).

Auto-registration relies on ACM's [agent registration](https://docs.redhat.com/en/documentation/red_hat_advanced_cluster_management_for_kubernetes/2.12/html/clusters/cluster_mce_overview#importing-managed-agent) method for importing clusters. That method allows fetching the Kubernetes resource manifests to install ACM's klusterlet agent and registering the agent through calls to a REST API. This REST API can be set up as configuration source for devices by creating a Repository resource and referencing that resource from the fleet's device template.

This document is intended for ACM and FlightCtl developers, QE engineers, and users who want to explore this feature. It provides step-by-step instructions, assuming minimal prior knowledge of ACM or FlightCtl.

## 1 Enable `edge-manager` in ACM

### 1.1 Patch `edge-manager-preview` in MCH.

```console
oc patch mch multiclusterhub -n open-cluster-management \
       --type=json -p='[{"op": "add", "path": "/spec/overrides/components/-","value":{"name":"edge-manager-preview","enabled":true}}]'
```

[!NOTE] If you're testing with a [downstream ACM](https://quay.io/repository/acm-d/acm-custom-registry?tab=tags), you also need to add the following annotation to the MCH, otherwise agents will not be able to use downstream images.

```console
oc annotate mch multiclusterhub -n open-cluster-management mch-imageRepository='quay.io:443/acm-d'
```

### 1.2 Enable flightctl-plugin from Console.

Navigate to Home -> Overview -> DynamicPlugins -> flightctl-plugin and enable it. After a few seconds, the console will prompt you to refresh the page.

Once refreshed, you will see the `Edge Management` option in the left navigation menu.

## 2 Setup and Configure Flightctl

### 2.1 Install flightctl CLI

For detailed instructions on how to install the flightctl CLI, please refer to the [Installing the Flight Control CLI](../getting-started.md#installing-the-flight-control-cli) section in the Getting Started guide.

### 2.2 Login to flightctl.

```console
export FLIGHTCTL_SERVER_ADDRESS=$(oc get route flightctl-api-route -n open-cluster-management -o jsonpath='{.spec.host}')

flightctl login $FLIGHTCTL_SERVER_ADDRESS --insecure-skip-tls-verify --token=<cluster admin token>
```

After flightctl is enabled, the Repository `acm-registration` will be created automatically.

### 2.3 Adding Auto-Registration Configuration to a Fleet's Device Template

To enable auto-registration in a fleet, add configuration items to the fleet's device template as shown in the following example:

```console
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: fleet-acm
spec:
  selector:
    matchLabels:
      fleet: acm
  template:
    spec:
      os:
        image: quay.io/<your org or user>/centos-bootc-flightctl:test
      config:
      - name: acm-crd
        httpRef:
          filePath: /var/local/acm-import/crd.yaml
          repository: acm-registration
          suffix: /agent-registration/crds/v1
      - name: acm-import
        httpRef:
          filePath: /var/local/acm-import/import.yaml
          repository: acm-registration
          suffix: /agent-registration/manifests/{{.metadata.name}}
      - name: pull-secret
        inline:
        - path: "/etc/crio/openshift-pull-secret"
          content: "{\"auths\":{...}}"
      - name: apply-acm-manifests
        inline:
        - path: "/etc/flightctl/hooks.d/afterupdating/50-acm-registration.yaml"
          content: |
            - if:
              - path: /var/local/acm-import/crd.yaml
                op: [created]
              run: kubectl apply -f /var/local/acm-import/crd.yaml
              envVars:
                KUBECONFIG: /var/lib/microshift/resources/kubeadmin/kubeconfig
            - if:
              - path: /var/local/acm-import/import.yaml
                op: [created]
              run: kubectl apply -f /var/local/acm-import/import.yaml
              envVars:
                KUBECONFIG: /var/lib/microshift/resources/kubeadmin/kubeconfig
```

The added items under `.spec.template.spec.config` have the following functions:

- `acm-crd` uses the HTTP Configuration Provider to query the ACM agent-registration server for the Kubernetes manifests containing the custom resource definition (CRD) for ACM's klusterlet agent. These manifests are stored in the device's filesystem in the file `/var/local/acm-import/crd.yaml`.
- `acm-import` queries the server once more to receive the import manifests for a cluster whose name is the same as the device's name, so both can be more easily correlated later. This is achieved by using the templating variable `{{ .metadata.name }}`. The returned manifests are stored in the same location on the device's filesystem as `import.yaml`.
- `pull-secret` optionally adds your OpenShift pull secret to the device, so MicroShift can pull the ACM agent's images from the container registry. You can download your pull secret from the [OpenShift installation page](https://cloud.redhat.com/openshift/install/pull-secret). This item is not necessary if you've already provisioned your pull secret in another way, for example by embedding it into the OS image. Also, you can use other configuration providers to add this secret.
  - [!NOTE] If you're using a downstream ACM, you also need to add `quay.io:443` registry in your pull secret.
- `apply-acm-manifests` installs an `afterUpdating` device lifecycle hook (see [Using Device Lifecycle Hooks](../managing-devices.md#using-device-lifecycle-hooks)). This hook gets called once after the agent has created the `crd.yaml` and `import.yaml` files and applies the manifests to the MicroShift cluster using the `kubectl` CLI.
- `image`: in test environment, you can use the same image built in [3.3](#33-build-the-image) to register the device.

## 3 Image Build

The image build is based on MacOS ARM64. If you're using a different platform, you can modify the commands to suit your needs.

To make it easier for readers to get started, we've prepared the related files in the same directory as this document.

### 3.1 Setup Podman.

Follow this [guide](https://podman-desktop.io/docs/podman/setting-podman-machine-default-connection) to set your podman into rootful mode.

```console
podman system connection default podman-machine-default-root
```

### 3.2 Download flightctl config file.

```console
flightctl certificate request --signer=enrollment --expiration=365d --output=embedded > config.yaml
```

### 3.3 Build the image.

```console
OCI_REGISTRY=quay.io
OCI_IMAGE_REPO=${OCI_REGISTRY}/<your org or user>/centos-bootc-flightctl
OCI_IMAGE_TAG=test

# build and push the container image
podman build -f Containerfile -t ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG} .

podman push ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}

# build disk image
mkdir -p output && \
	 podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v $(pwd)/output:/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type qcow2 --local \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

### 3.4 Start a simulator.

```console
qemu-img resize ./output/qcow2/disk.qcow2 +20G && \
qemu-system-aarch64 \
    -M accel=hvf \
    -cpu host \
    -smp 2 \
    -m 8096 \
    -bios /opt/homebrew/Cellar/qemu/9.2.0/share/qemu/edk2-aarch64-code.fd \
    -serial stdio \
    -machine virt \
    -snapshot ./output/qcow2/disk.qcow2
```

Once the simulator is running, you can run the following commands to check the progress of the device enrollment.

```console
systemctl status microshift
systemctl status flightctl-agent

# check logs of flightctl-agent service
journalctl -u microshift.service -n 20 --no-pager
journalctl -u flightctl-agent.service -n 20 --no-pager

export KUBECONFIG=/var/lib/microshift/resources/kubeadmin/kubeconfig
kubectl get pod -A # should see ocm ns and another resources created

cat /etc/crio/openshift-pull-secret

# once the device is enrolled, you will see the following files in the device.
ls /var/local/acm-import/
```

### 3.5 Approve the device.

When you approve a device, make sure you set the label `fleet=acm`. Afther the device is approved in `Edge Management`, you will see a same name cluster registered and automatically approved in ACM.

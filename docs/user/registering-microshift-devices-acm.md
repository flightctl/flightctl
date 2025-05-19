# Auto-Registering Devices with MicroShift into ACM

If you have fleets of devices running an OS image that includes MicroShift, you can configure these fleets to auto-register MicroShift clusters with Red Hat Advanced Cluster Management (ACM).

Auto-registration relies on ACM's [agent registration](https://docs.redhat.com/en/documentation/red_hat_advanced_cluster_management_for_kubernetes/2.12/html/clusters/cluster_mce_overview#importing-managed-agent) method for importing clusters. That method allows fetching the Kubernetes resource manifests to install ACM's klusterlet agent and registering the agent through calls to a REST API. This REST API can be set up as configuration source for devices by creating a Repository resource and referencing that resource from the fleet's device template.

## Auto-Registering a Fleet's Devices using the Web UI

## Auto-Registering a Fleet's Devices using the CLI

### Creating the ACM Registration Repository

> [!NOTE] When using ACM with integrated Flight Control, the creation of this repository happens automatically when the Flight Control service is deployed, so this section can be skipped.

To set up auto-registration using the CLI, follow the procedure for ["Importing a managed cluster by using agent registration"](https://docs.redhat.com/en/documentation/red_hat_advanced_cluster_management_for_kubernetes/2.12/html/clusters/cluster_mce_overview#importing-managed-agent) in ACM's documentation to configure the necessary Role-Based Access Control (RBAC) policies for your user and obtain the required registration information. Skip the last step of the actual cluster import, which will be handled by auto-registration.

After following the procedure, you should have the following information available and stored in shell variables:

- `${agent_registration_host}`: The hostname part of ACM's agent registration server URL.
- `${cacert}`: The path to the `ca.crt` file for ACM's agent registration server.
- `${token}`: The bearer token for accessing ACM's agent registration server.

With these variables defined, create a Repository resource manifest file `acm-registration-repo.yaml` for accessing the agent registration server by running the following command:

```console
cat <<- EOF > acm-registration-repo.yaml
  apiVersion: flightctl.io/v1alpha1
  kind: Repository
  metadata:
    name: acm-registration
  spec:
    httpConfig:
      token: ${token}
      ca.crt: $(base64 -w0 < ${cacert})
    type: http
    url: https://${agent_registration_host}
    validationSuffix: /agent-registration/crds/v1
EOF
```

Create the Repository resource by applying the file:

```console
flightctl apply -f acm-registration-repo.yaml
```

### Adding Auto-Registration Configuration to a Fleet's Device Template

To enable auto-registration in a fleet, add configuration items to the fleet's device template as shown in the following example:

```console
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: fleet-acm
spec:
  template:
    spec:
      os:
        image: quay.io/someorg/someimage-with-microshift:v1
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
- `apply-acm-manifests` installs an `afterUpdating` device lifecycle hook (see [Using Device Lifecycle Hooks](managing-devices.md#using-device-lifecycle-hooks)). This hook gets called once after the agent has created the `crd.yaml` and `import.yaml` files and applies the manifests to the MicroShift cluster using the `kubectl` CLI.

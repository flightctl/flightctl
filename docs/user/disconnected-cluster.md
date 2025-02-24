# Installing a Flight Control in a Disconnected Cluster Environment

## Mirroring images

### Mirroring flightctl images

```shell
export FCTL_VERSION=<required_flightctl_version>
export LOCAL_REGISTRY=<registry-hostname>:<registry-port>
```

> 📌 Please note, that you may need to provide credentials for your local registry via `oc image mirror -a <creds_file>`.

```shell
for component in api periodic ui worker; do oc image mirror quay.io/flightctl/flightctl-$component:${FCTL_VERSION} ${LOCAL_REGISTRY}/flightctl/flightctl-$component:${FCTL_VERSION}; done
```

If you cannot connect directly to hub's registry, you will have to mirror images to a disk or USB stick and bring the mirrored content to the disconnected management hub. Then mirror the content to a hub's registry.

Example:

```shell
#on machine connected to internet
oc image mirror quay.io/<img> file://path/<img>
#on disconnected environment
oc image mirror file://path/<img> ${LOCAL_REGISTRY}/<img>
```

### Mirroring other images

```shell
oc image mirror quay.io/keycloak/keycloak:25.0.1 ${LOCAL_REGISTRY}/keycloak/keycloak:25.0.1

oc image mirror quay.io/openshift/origin-cli:4.20.0 ${LOCAL_REGISTRY}/openshift/origin-cli:4.20.0

oc image mirror quay.io/sclorg/postgresql-16-c9s:20250214 ${LOCAL_REGISTRY}/sclorg/postgresql-16-c9s:20250214

oc image mirror docker.io/redis:7.4.1 ${LOCAL_REGISTRY}/redis:7.4.1
```

## Configuring image repository mirroring

Create `ImageTagMirrorSet` with the following content

```shell
oc apply -f - <<EOF
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
  name: image-mirror
spec:
  imageTagMirrors:
  - source: quay.io/flightctl
    mirrors:
    - ${LOCAL_REGISTRY}/flightctl
  - source: quay.io/sclorg
    mirrors:
    - ${LOCAL_REGISTRY}/sclorg
  - source: quay.io/keycloak
    mirrors:
    - ${LOCAL_REGISTRY}/keycloak
  - source: quay.io/openshift/origin-cli
    mirrors:
    - ${LOCAL_REGISTRY}/openshift/origin-cli
  - source: docker.io/redis
    mirrors:
    - ${LOCAL_REGISTRY}/redis
EOF
```

After creating/editing `ImageTagMirrorSet`, the OpenShift will drain all nodes. You will need to wait for all nodes to return to `Ready` state.

## Installing flightctl

You need to download the helm chart first, as it is stored in quay.io

```shell
#on machine connected to internet
helm pull oci://quay.io/flightctl/charts/flightctl:${FCTL_VERSION}
#copy the downloaded file to disconnected environment
#on disconnected environment
helm upgrade --install --namespace flightctl --create-namespace flightctl ./flightctl-${FCTL_VERSION}.tgz
```

If you modify any image location/tag in helm chart, you need to ensure that you run `oc image mirror` for the modified image location/tag and `ImageTagMirrorSet` has proper mirrors set.

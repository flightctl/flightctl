# Installing a Flight Control in a Disconnected Cluster Environment

> 📌 Please note, that version `0.4.0-rc3` are used for demonstration purposes only

> 📌 All `sha256` are valid for `0.4.0-rc3` version only

## Log in

### login to hub

```shell
ssh root@<hub-hostname>
```

### all operations should be executed from `kni` user

```shell
su - kni
```

### login to cluster

```shell
export KUBECONFIG=clusterconfigs/auth/kubeconfig
```

## Configure mirroring `icsp`

### check whether `quay.io` mirror is configured in `icsp`

```shell
oc get imagecontentsourcepolicy image-policy -o yaml
```

look for

```yaml
  - mirrors:
    - <registry-hostname>:5000
    source: quay.io
```

if not present, add it using

```shell
oc edit imagecontentsourcepolicy image-policy
```

## Mirror quay.io images

### see list of images to use

```shell
helm show values oci://quay.io/flightctl/charts/flightctl --version=0.4.0-rc3
```

### mirror flightctl images

```shell
oc image mirror -a combined-secret.json quay.io/flightctl/flightctl-worker:0.4.0-rc3  <registry-hostname>:5000/flightctl/flightctl-worker:0.4.0-rc3

oc image mirror -a combined-secret.json quay.io/flightctl/flightctl-periodic:0.4.0-rc3  <registry-hostname>:5000/flightctl/flightctl-periodic:0.4.0-rc3

oc image mirror -a combined-secret.json quay.io/flightctl/flightctl-ui:0.4.0-rc3  <registry-hostname>:5000/flightctl/flightctl-ui:0.4.0-rc3

oc image mirror -a combined-secret.json quay.io/flightctl/flightctl-api:0.4.0-rc3  <registry-hostname>:5000/flightctl/flightctl-api:0.4.0-rc3
```

### mirror `keycloak`, `redis` and `postgresql` (please note: format of `keycloak` in `values.yaml` is different from others)

```shell
oc image mirror -a combined-secret.json quay.io/keycloak/keycloak:25.0.1 <registry-hostname>:5000/keycloak/keycloak:25.0.1

oc image mirror -a combined-secret.json quay.io/sclorg/redis-7-c9s:latest <registry-hostname>:5000/sclorg/redis-7-c9s
```

> 📌 Note: PostgreSQL Version Upgrade will occur in `0.5.0`, so `postgresql-12-c8s` will not be required

```shell
oc image mirror -a combined-secret.json quay.io/sclorg/postgresql-12-c8s:latest <registry-hostname>:5000/sclorg/postgresql-12-c8s

oc image mirror -a combined-secret.json quay.io/sclorg/postgresql-16-c9s:latest <registry-hostname>:5000/sclorg/postgresql-16-c9s
```

### `sha256`

Look for (usually the last line)

```kubernetes helm
sha256:b33dedfeb245199a03122505d35d9c75ae01d77ffe39e0950ed98b0d02ecdf64 <registry-hostname>:5000/flightctl/flightctl-kv:latest
```

and add it to `values.yaml` ([See Example](#an-example-of-valuesyaml)). Please note

- `@ha256` must be placed at the end of image name
- `sha256`'s value must be placed under `tag`

## Install flightctl

### clear previous installation if required

```shell
helm uninstall flightctl -n flightctl

kubectl delete pvc --all -n flightctl
```

### install

```shell
helm upgrade --install --version=0.4.0-rc3  --namespace flightctl --create-namespace flightctl oci://quay.io/flightctl/charts/flightctl --values values.yaml
```

## Troubleshooting

### watch `pods` starting up

Pod startup failures are expected due to dependencies between components.
This happens because some components rely on others to be fully ready.

```shell
oc get pods -n flightctl -w
```

### get description

```shell
oc describe pods flightctl-api-546ff9d5b9-ccf8c -n flightctl
```

### List all pods excluding those with 'run' or 'comp' in their status (typically Running or Completed)

```shell
oc get pods -A |grep -vEi "run|comp"
```

### An example of `values.yaml`

```yaml
kv:
  enabled: true
  image:
    image: "<registry-hostname>:5000/sclorg/redis-7-c9s@sha256"
    pullPolicy: Always
    tag: "d772acab11507231a2b152850f47c8b9b859dedb6652ca624949e97cb28c5e6f"

keycloak:
  image: "<registry-hostname>:5000/keycloak/keycloak@sha256:a0c0dedfee3b1b4fae57a0b0c6f215616012eb99f12201c43a8f0444f95c205d"
  db:
    image: "<registry-hostname>:5000/sclorg/postgresql-16-c9s@sha256:e17368aa6174b27d968043cf1fd8b3b17b26b486e7ca84534bf6feffa0830523"

db:
  storage:
    size: "2Gi"
  image:
    image: "<registry-hostname>:5000/sclorg/postgresql-12-c8s@sha256"
    pullPolicy: Always
    tag: "63bee816dcef5e3db93087bc2a39e91ae21f55a142b6231b7f69ba541b4d435a"

api:
  enabled: true
  image:
    image: "<registry-hostname>:5000/flightctl/flightctl-api@sha256"
    pullPolicy: Always
    tag: "ed4c458f6901f251beb0461e042f07126d98a579fdf4c09def1c7428d17cad25"

worker:
  image:
    image: "<registry-hostname>:5000/flightctl/flightctl-worker@sha256"
    pullPolicy: Always
    tag: "d87bbad85f740a70482d0e21c5b7ddf47e9cdcc37c175adfe30b3707e8c4cdbc"

ui:
  image:
    image: "<registry-hostname>:5000/flightctl/flightctl-ui@sha256"
    pullPolicy: Always
    tag: "896ac2f6e9bffc833a1839de10cab23c7597873ba0422920db2db0221be25016"

periodic:
  image:
    image: "<registry-hostname>:5000/flightctl/flightctl-periodic@sha256"
    pullPolicy: Always
    tag: "c56f96cf5ec9d223068a2e9e273ec986be0982764fb7e8db07ec2bc5741db9bc"
```

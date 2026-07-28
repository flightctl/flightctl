# Configuring VM application rendering

The Flight Control worker converts `VmApplication` manifests into Quadlet units with `vm-to-quadlet` before devices receive the rendered application. You can configure the virt-launcher image and passt workarounds used during that conversion.

## Defaults

| Setting | Default |
| ------- | ------- |
| `launcherImage` | `quay.io/kubevirt/virt-launcher:v1.8.4` |
| `passtWorkarounds` | `true` |

`passtWorkarounds` enables startup patches for known networking issues in older virt-launcher passt builds (for example guest network instability and related passt failures). Keep this enabled unless your virt-launcher image already includes a fixed passt build.

## Prerequisites

- Flight Control deployed with the worker service (`flightctl-worker`)

## Configuration reference

| Parameter | Type | Description |
| --------- | ---- | ----------- |
| `worker.vmRender.launcherImage` | string | Container image reference for the KubeVirt virt-launcher compute container. |
| `worker.vmRender.passtWorkarounds` | boolean | When `true`, enable passt networking workarounds in generated Quadlet units. |

## Kubernetes (Helm)

Set the values under `worker.vmRender`:

```yaml
worker:
  vmRender:
    launcherImage: "quay.io/kubevirt/virt-launcher:v1.8.4"
    passtWorkarounds: true
```

Apply the chart upgrade, then restart the worker so it reloads configuration:

```bash
helm upgrade flightctl ./deploy/helm/flightctl -n <namespace> -f values.yaml
kubectl rollout restart deployment/flightctl-worker -n <namespace>
```

## Podman Quadlet

Edit `/etc/flightctl/service-config.yaml`:

```yaml
worker:
  vmRender:
    launcherImage: "quay.io/kubevirt/virt-launcher:v1.8.4"
    passtWorkarounds: true
```

Restart the worker so the config template is re-rendered and the service reloads:

```bash
sudo systemctl restart flightctl-worker.service
```

## After changing settings

Conversion results are cached by `vm.yaml` content and these render options. Changing `launcherImage` or `passtWorkarounds` affects newly rendered devices. Re-render existing devices (for example by updating the device or fleet) if they must pick up the new settings.

## Related information

- [VM applications](../using/managing-devices.md#vm-applications)

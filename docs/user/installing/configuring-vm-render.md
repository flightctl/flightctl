# Configuring VM application rendering

The Flight Control worker converts `VmApplication` manifests into Quadlet units with `vm-to-quadlet` before devices receive the rendered application. You can configure the virt-launcher image and passt workarounds used during that conversion.

## Defaults

| Setting | Default |
| ------- | ------- |
| `launcherImage` | `quay.io/kubevirt/virt-launcher:v1.9.0` (built into the worker; leave config unset or empty to use it) |
| `passtWorkarounds` | `false` |

`passtWorkarounds` enables startup patches for known networking issues in older virt-launcher passt builds (for example guest network instability and related passt failures). The default virt-launcher image does not need this. Enable it only when you override `launcherImage` to an older image that still requires the workaround.

## Prerequisites

- Flight Control deployed with the worker service (`flightctl-worker`)

## Configuration reference

| Parameter | Type | Description |
| --------- | ---- | ----------- |
| `worker.vmRender.launcherImage` | string | Container image reference for the KubeVirt virt-launcher compute container. Leave empty to use the worker built-in default. |
| `worker.vmRender.passtWorkarounds` | boolean | When `true`, enable passt networking workarounds in generated Quadlet units. |

## Kubernetes (Helm)

Set the values under `worker.vmRender`. Leave `launcherImage` empty unless you need an override:

```yaml
worker:
  vmRender:
    launcherImage: ""
    passtWorkarounds: false
```

Apply the chart upgrade, then restart the worker so it reloads configuration:

```bash
helm upgrade flightctl ./deploy/helm/flightctl -n <namespace> -f values.yaml
kubectl rollout restart deployment/flightctl-worker -n <namespace>
```

## Podman Quadlet

Edit `/etc/flightctl/service-config.yaml` only when you need overrides. Omit `launcherImage` to keep the worker default:

```yaml
worker:
  vmRender:
    passtWorkarounds: false
```

Restart the worker so the config template is re-rendered and the service reloads:

```bash
sudo systemctl restart flightctl-worker.service
```

## Air-gapped environments

The virt-launcher image is pulled by devices when they run VM applications. `flightctl-mirror-images` includes it in the default image set:

| Variant | Image |
| ------- | ----- |
| Community (`community-el9`, `community-el10`) | `quay.io/kubevirt/virt-launcher:v1.9.0` |
| Red Hat product (`rhem-el9`, `rhem-el10`) | `registry.redhat.io/container-native-virtualization/virt-launcher-rhel9:v4.22-1784929080` |

The worker still writes the original image reference into rendered Quadlet units. After mirroring, set `worker.vmRender.launcherImage` to the destination reference so devices pull from the mirror. Guest OS and other workload images still need separate mirroring.

In an air-gapped deployment:

1. Run `flightctl-mirror-images` for your variant. The tool prints the mirrored virt-launcher reference.
2. Set `worker.vmRender.launcherImage` to that destination reference.
3. Restart the worker, then re-render devices that already have VM applications so they pick up the new image reference.

Example (community):

```yaml
worker:
  vmRender:
    launcherImage: "registry.example.com:5000/kubevirt/virt-launcher:v1.9.0"
    passtWorkarounds: false
```

Example (Red Hat product):

```yaml
worker:
  vmRender:
    launcherImage: "registry.example.com:5000/container-native-virtualization/virt-launcher-rhel9:v4.22-1784929080"
    passtWorkarounds: false
```

If devices already remap the source registry (for example with `registries.conf` or `ImageTagMirrorSet`), you can keep the original source reference instead of the destination path.

> [!IMPORTANT]
> The Red Hat product image is on `registry.redhat.io`, which requires authentication. Place Red Hat registry credentials in the Podman `auth.json` on each device before the device starts a VM application. For Quadlet units that run as root, the file is `/root/.config/containers/auth.json`. See [Using image pull secrets](../using/managing-devices.md#using-image-pull-secrets). Devices that pull only from an internal mirror need credentials for that registry instead.

## After changing settings

Conversion results are cached by `vm.yaml` content and these render options. Changing `launcherImage` or `passtWorkarounds` affects newly rendered devices. Re-render existing devices (for example by updating the device or fleet) if they must pick up the new settings.

## Related information

- [VM applications](../using/managing-devices.md#vm-applications)
- [Using image pull secrets](../using/managing-devices.md#using-image-pull-secrets)
- [Air-gapped installation](air-gapped-installation.md)

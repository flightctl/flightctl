# Configuring VM application rendering

The Flight Control worker converts `VmApplication` manifests into Quadlet units with `vm-to-quadlet` before devices receive the rendered application. You can configure the virt-launcher image, optional per-OS images, and passt workarounds used during that conversion.

## Defaults

| Setting | Default |
| ------- | ------- |
| `launcherImage` | `quay.io/kubevirt/virt-launcher:v1.9.0` (built into the worker; leave config unset or empty to use it) |
| `launcherImages` | empty (no per-OS pins) |
| `passtWorkarounds` | `false` |

`passtWorkarounds` enables startup patches for known networking issues in older virt-launcher passt builds (for example guest network instability and related passt failures). The default virt-launcher image does not need this. Enable it only when the selected `launcherImage` or `launcherImages` entry is an older image that still requires the workaround.

## How the worker selects an image

At render time the worker builds a key from device `status.systemInfo`:

- `distroId` (os-release `ID`, for example `rhel` or `fedora`)
- The leading major digits of `distroVersion` (os-release `VERSION`, for example `9.5` yields `9`)

The key is `{distroId}-{major}`. Examples:

| Device reports | Key |
| -------------- | --- |
| `distroId: rhel`, `distroVersion: 9.5` | `rhel-9` |
| `distroId: rhel`, `distroVersion: 10.0` | `rhel-10` |
| `distroId: fedora`, `distroVersion: 42` | `fedora-42` |

The worker then chooses an image in this order:

1. The matching entry in `launcherImages`, if that key is present and non-empty.
2. `launcherImage`, if set.
3. The built-in default.

Keys are exact. A Fedora 42 device does not use a `rhel-9` pin. Devices that do not report `distroId` (or whose OS is not in the map) use `launcherImage` or the built-in default.

## Prerequisites

- Flight Control deployed with the worker service (`flightctl-worker`)

## Configuration reference

| Parameter | Type | Description |
| --------- | ---- | ----------- |
| `worker.vmRender.launcherImage` | string | Default virt-launcher image when `launcherImages` has no match. Leave empty to use the worker built-in default. |
| `worker.vmRender.launcherImages` | object | Optional map of virt-launcher images keyed by `{distroId}-{major}` from `status.systemInfo` (for example `"rhel-9"`, `"rhel-10"`). |
| `worker.vmRender.passtWorkarounds` | boolean | When `true`, enable passt networking workarounds in generated Quadlet units. |

## Kubernetes (Helm)

Set the values under `worker.vmRender`. Leave `launcherImage` empty unless you need an override. Leave `launcherImages` empty unless you pin images per OS:

```yaml
worker:
  vmRender:
    launcherImage: ""
    launcherImages: {}
    passtWorkarounds: false
```

To pin images for mixed OS fleets:

```yaml
worker:
  vmRender:
    launcherImage: ""
    launcherImages:
      "rhel-9": "<registry>/virt-launcher-rhel9:<tag>"
      "rhel-10": "<registry>/virt-launcher-rhel10:<tag>"
    passtWorkarounds: false
```

Replace `<registry>` and `<tag>` with the image repository and tag that devices can pull.

Apply the chart upgrade, then restart the worker so it reloads configuration:

```bash
helm upgrade flightctl ./deploy/helm/flightctl -n <namespace> -f values.yaml
```

```bash
kubectl rollout restart deployment/flightctl-worker -n <namespace>
```

## Podman Quadlet

Edit `/etc/flightctl/service-config.yaml` only when you need overrides. Omit `launcherImage` to keep the worker default:

```yaml
worker:
  vmRender:
    passtWorkarounds: false
```

To pin images per OS, add `launcherImages` under the same `vmRender` block:

```yaml
worker:
  vmRender:
    launcherImages:
      "rhel-9": "<registry>/virt-launcher-rhel9:<tag>"
      "rhel-10": "<registry>/virt-launcher-rhel10:<tag>"
    passtWorkarounds: false
```

Restart the worker so the config template is re-rendered and the service reloads:

```bash
sudo systemctl restart flightctl-worker.service
```

## Air-gapped environments

The default virt-launcher image is pulled by devices when they run VM applications. It is not part of the control-plane image set produced by `flightctl-mirror-images`.

In an air-gapped deployment:

1. Mirror each virt-launcher image you will use to a registry that devices can reach. Mirror the default image and every image you pin in `launcherImages`.
2. Set `worker.vmRender.launcherImage` to the mirrored default. Set `worker.vmRender.launcherImages` to the mirrored per-OS references.
3. Restart the worker, then re-render devices that already have VM applications so they pick up the new image references.

Example (community / upstream registry):

```bash
INTERNAL=registry.example.com:5000
```

```bash
skopeo copy --all \
  docker://quay.io/kubevirt/virt-launcher:v1.9.0 \
  docker://${INTERNAL}/kubevirt/virt-launcher:v1.9.0
```

```yaml
worker:
  vmRender:
    launcherImage: "registry.example.com:5000/kubevirt/virt-launcher:v1.9.0"
    launcherImages:
      "rhel-9": "registry.example.com:5000/kubevirt/virt-launcher-rhel9:<tag>"
      "rhel-10": "registry.example.com:5000/kubevirt/virt-launcher-rhel10:<tag>"
    passtWorkarounds: false
```

Omit `launcherImages` if every device should use the single mirrored `launcherImage`.

There is currently no separate Red Hat product virt-launcher image in the Flight Control packaging set. Use the upstream `quay.io/kubevirt/virt-launcher` reference, or another virt-launcher image you supply, and point `launcherImage` and `launcherImages` at the mirrored location.

## After changing settings

Conversion results are cached by `vm.yaml` content and these render options. Changing `launcherImage`, `launcherImages`, or `passtWorkarounds` affects newly rendered devices. Re-render existing devices (for example by updating the device or fleet) if they must pick up the new settings.

## Related information

- [VM applications](../using/managing-devices.md#vm-applications)
- [Air-gapped installation](air-gapped-installation.md)

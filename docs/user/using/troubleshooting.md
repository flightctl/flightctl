# Troubleshooting

## Verifying the effective device specification received by the device agent

When viewing a device resource using the command

```console
flightctl get device/${device_name} -o yaml
```

the output contains the device specification as specified by the user or the fleet controller based on the fleet's device template. That specification may contain references to configuration or secrets stored on external systems, such a Git or a Kubernetes cluster.

Only when the device agent queries the service, the service replaces these references with the actual configuration and secret data. While this better protects potentially sensitive data, it also makes troubleshooting faulty configurations hard.

Users with the `GetRenderedDevice` permission can run the following command to view the effective configuration as rendered by the service to the device agent:

```console
flightctl get device/${device_name} -o yaml --rendered
```

## Troubleshooting delta generation and delivery

Use these checks to diagnose generation, registry access, and delta downloads.

### Verifying delta registry access

Delta generation reads the source and target images from the image repository and publishes the generated delta artifact to the configured delta storage target. Verify both locations when troubleshooting generation or download failures:

1. Inspect the device status using `flightctl get device <device_name> -o yaml`. For OS updates, check the reported source digest in `status.os.imageDigest`. For applications, check the image references and digests in `status.applications[].imageDigests[]`. Images with a missing source digest use regular image preparation; generation requires a known source digest.
2. Verify that the reported source image and the desired target image are available by digest in the target image's repository. For multi-platform images, check the manifests for the device's platform. Configure the registry retention policy to preserve the source manifest and its layers until the update completes. The control plane needs pull access to these images with the configured credentials and TLS trust.
3. Identify the configured [delta storage target](managing-repositories.md#configuring-a-delta-storage-target). An organization OCI Repository marked with `deltaStorageTarget: true` takes precedence over the deployment's `deltaGeneration.defaultRepository`.
4. Verify that the delta storage registry is reachable from both the control plane and the device agent. The control plane needs read and push access; the agent needs pull access. Check the registry credentials and TLS trust used by each.
5. Inspect the generated delta reference using the fields in [Inspecting delta image references](#inspecting-delta-image-references). Verify that the referenced artifact is available in the delta storage repository and identifies the expected source and target image pair for the device's platform.

For CI-published deltas, verify the delta referrer in the target image's repository using the publication requirements in [Using CI-published OS deltas](managing-devices.md#using-ci-published-os-deltas).

### Inspecting delta generation progress

Inspect [Delta preparation events](../references/events.md#delta-preparation-events) for the Fleet or standalone Device being updated:

```console
flightctl get events --field-selector="involvedObject.kind=Fleet,involvedObject.name=<fleet_name>,reason=DeltaGenerationProgress" -o yaml
```

For a standalone device, use `involvedObject.kind=Device` and its name. Each event identifies the image pair through `details.imageRepository`, `details.sourceDigest`, and `details.targetDigest`. `details.generationStatus` reports `in_progress`, `succeeded`, `failed`, or `rejected`. Successful, failed, and rejected pairs are terminal and count toward `status.deltaGeneration.completed`.

While a pair is in progress, `details.phase` identifies the current step:

| Phase | Check |
| ----- | ----- |
| `checkingExisting` | Registry access while checking for an existing delta. |
| `pullSource` | Source image digest, manifests, layers, and pull credentials. |
| `pullTarget` | Target image digest, manifests, layers, and pull credentials. |
| `createDelta` | Delta-worker logs for generation errors. |
| `push` | Delta storage destination, push credentials, and TLS trust. |

Phases are reported when they change. `status.deltaGeneration.lastUpdated` records the last counter update. Use worker logs for failure details and rejected-job diagnostics.

On Kubernetes, inspect the delta-worker Deployment logs:

```console
kubectl logs --namespace <installation_namespace> deployment/flightctl-delta-worker
```

On Linux, inspect the delta-worker service logs:

```console
sudo journalctl -u flightctl-delta-worker.service --no-pager
```

### Inspecting delta image references

Use the [effective device specification](#verifying-the-effective-device-specification-received-by-the-device-agent) with `--rendered` to inspect the delta artifact references that Flight Control supplies to the agent. Flight Control manages these read-only fields automatically. A reference appears when a generated delta is available for that target. For targets without a generated reference, the agent uses CI-referrer discovery and regular image preparation:

| Field | Description |
| ----- | ----------- |
| `spec.os.deltaImage` | Generated delta for the target OS image. |
| `spec.applications[].deltaImage` | Generated delta for the application's main image. |
| `spec.applications[].deltaImages[].targetImage` | Nested application image reference associated with a generated delta. |
| `spec.applications[].deltaImages[].targetDigest` | Target digest for that nested image. |
| `spec.applications[].deltaImages[].deltaImage` | Generated delta artifact for that nested image. |

For example, the application portion of the output for a container application with an image-backed volume can include the following. Digests are truncated for readability:

```yaml
spec:
  applications:
    - name: telemetry
      appType: container
      image: quay.io/example/telemetry:v2
      deltaImage: quay.io/example-deltas/telemetry@sha256:ddd...
      deltaImages:
        - targetImage: quay.io/example/telemetry-content:v2
          targetDigest: sha256:ccc...
          deltaImage: quay.io/example-deltas/telemetry-content@sha256:eee...
      volumes:
        - name: content
          image:
            reference: quay.io/example/telemetry-content:v2
```

In this example, `deltaImages[0].targetImage` matches the `content` volume's `image.reference`. Its `deltaImage` is the generated delta for that nested target.

The agent uses generated delta references or discovers matching CI-published deltas for each image target. Inspect update results in [OS delta status](../references/device-api-statuses.md#os-delta-status) and [Application delta status](../references/device-api-statuses.md#application-delta-status).

## Generating and downloading an SOS report

SOS reports are a collection of system information that can be used to debug issues with the device agent. The SOS report is generated by the `sos report` command, which is included in the device image.
To generate an SOS report, run the following `console` command from the CLI:

```console
flightctl console device/${device_name} -- sudo sos report --batch --quiet
```

The output is a tarball named `sosreport-$hostname-$timestamp-$random-sequence.tar.xz`, whereby `$hostname` is the device's hostname and `$timestamp` is the current date and time. The tarball contains system information, logs, and configuration files that can be used to debug issues with the device agent.

Output example:

```console
sos report (version 4.8.1)


Your sos report has been generated and saved in:
       /var/tmp/sosreport-localhost-2025-04-28-svjuich.tar.xz

 Size  11.83MiB
 Owner root
 sha256        918563a260c5d6a069e178e9ddce643b3b93ad2a4fdca9a3e431b63b2e82041d

Please send this file to your support representative.
```

To download the SOS report, run the following `console` command from the CLI:

```console
flightctl console device/${device_name} -- sudo cat /var/tmp/sosreport-localhost-2025-04-28-svjuich.tar.xz > sosreport.tar.xz
```

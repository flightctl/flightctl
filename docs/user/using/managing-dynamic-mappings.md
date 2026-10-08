# Managing dynamic label mappings

Dynamic label mappings promote selected device-reported values into device labels. Use these labels to select fleets and rollout batches, render fleet templates, and filter device inventories.

## How mappings work

A `LabelSyncMapping` is an organization-scoped resource. It evaluates a Common Expression Language (CEL) expression against a device's current `metadata`, `spec`, and `status`. The result is written to the device's labels and can be used anywhere Flight Control uses labels.

Dynamic mappings are separate from the agent's enrollment-time `label-from-systeminfo` configuration. Enrollment-time mappings add labels when a device enrolls. A `LabelSyncMapping` can update labels later when Flight Control receives a device status update. The mapping does not change how the agent collects or reports system information.

Fleet templates use promoted labels through `.metadata.labels`. They do not expose raw `systemInfo` or `customInfo` fields as template variables.

## Default mappings

Helm and quadlet deployments provide a starter set of mappings. The default mappings include:

| Mapping | Default output |
|---|---|
| `system-info` | The `agentVersion`, `architecture`, `deltaEligible`, `distroId`, and `distroVersion` fields from `status.systemInfo`, as `feature.flightctl.io/systemInfo.<field>` labels. |
| `custom-info` | Entries from `status.systemInfo.customInfo` as `feature.flightctl.io/customInfo.<field>` labels. |
| `gpu-present` | `feature.flightctl.io/gpu.present` when the device reports GPUs. |
| `kvm-enabled` | `feature.flightctl.io/kvm.enabled` when the device reports KVM support. |
| `os-mode` | `feature.flightctl.io/os.mode` when the device reports its OS mode. |

The field prefixes in this table are chosen by the bundled mapping expressions. Flight Control does not add a prefix to mapping outputs. A map expression must return complete label keys, and a scalar mapping must set its complete destination key.

## Create a mapping

Set `spec.resourceType` to `Device`. Set `spec.key` to create a scalar mapping. Omit `spec.key` or set it to null to create a map mapping.

This scalar mapping writes the device's reported region to one complete label key:

```yaml
apiVersion: flightctl.io/v1beta1
kind: LabelSyncMapping
metadata:
  name: device-region
spec:
  resourceType: Device
  key: ops.example.com/region
  expression: status.systemInfo.customInfo.region
```

This map mapping transforms every custom information entry and includes the source key in the destination label key:

```yaml
apiVersion: flightctl.io/v1beta1
kind: LabelSyncMapping
metadata:
  name: custom-info
spec:
  resourceType: Device
  expression: |
    (has(status.systemInfo.customInfo) && status.systemInfo.customInfo != null
      ? status.systemInfo.customInfo
      : {}).transformMapEntry(
        k,
        v,
        {"ops.example.com/customInfo." + k: v}
      )
```

In a map expression, `transformMapEntry(k, v, result)` evaluates `result` for each key and value. You can add a Boolean condition before `result` to filter entries. The expression returns a map whose keys are complete label keys.

CEL expressions can use optional values. Use `has()` or a conditional expression to handle fields that might be absent. An absent field, a null result, an empty result, or `optional.none()` produces no label value for that device. A successful mapping with no value removes a previous label owned by that mapping.

Mapping results must be a scalar value or a map of scalar values. Nested maps, lists, and other complex results are rejected. Scalar values are converted to strings and sanitized for use as label values. Map keys must be valid label keys.

Each map expression can return up to 50 entries. A device can have up to 100 mapping-owned labels across all mappings. An expression or output that exceeds these limits is reported as a mapping failure.

## Manage mappings through the API

The REST API supports listing, creating, retrieving, replacing, patching, and deleting mappings. Requests operate in the organization selected by the caller's API identity.

| Operation | Method and path |
|---|---|
| List mappings | `GET /api/v1/labelsyncmappings` |
| Create a mapping | `POST /api/v1/labelsyncmappings` |
| Retrieve a mapping | `GET /api/v1/labelsyncmappings/{name}` |
| Replace a mapping | `PUT /api/v1/labelsyncmappings/{name}` |
| Patch a mapping | `PATCH /api/v1/labelsyncmappings/{name}` |
| Delete a mapping | `DELETE /api/v1/labelsyncmappings/{name}` |

Use the `Flightctl-API-Version: v1beta1` header when requesting this API version. List requests support the standard `limit` and `continue` parameters.

The Flight Control CLI does not currently expose `LabelSyncMapping` through its `get`, `apply`, or `edit` commands. Use the REST API to manage these resources.

For example, patch a mapping expression with a JSON Patch request:

```bash
curl --request PATCH \
  --header "Authorization: Bearer <access_token>" \
  --header "Flightctl-API-Version: v1beta1" \
  --header "Content-Type: application/json-patch+json" \
  --data-binary @mapping-patch.json \
  "<api_server>/api/v1/labelsyncmappings/device-region"
```

The patch file contains a JSON Patch document:

```json
[
  {
    "op": "replace",
    "path": "/spec/expression",
    "value": "status.systemInfo.customInfo.region"
  }
]
```

The mapping's resource type is immutable. Updating its key or expression increments its generation and sets its Ready condition to `Pending`. Invalid CEL or a result type that does not match scalar or map mode returns HTTP 422. A duplicate scalar destination key returns HTTP 409. A stale resource version can also return HTTP 409; retrieve the latest resource before retrying the update.

## Use mapped labels

Promoted labels use the same selectors as other device labels. This fleet selector targets devices whose reported region is east:

```yaml
spec:
  selector:
    matchLabels:
      feature.flightctl.io/customInfo.region: east
```

Use the same label key in a rollout batch selector. A fleet template can read its value with a Go template expression:

```yaml
spec:
  template:
    spec:
      config:
        - name: region
          inline:
            content: |
              region={{ index .metadata.labels "feature.flightctl.io/customInfo.region" }}
```

Filter the device inventory by a promoted label with the CLI:

```console
flightctl get devices -l feature.flightctl.io/customInfo.region=east
```

The web interface can also search and filter using promoted label keys. When a fleet selector, template, or rollout policy uses a promoted label, the interface displays a security warning. The warning applies to fleet configuration; the CLI does not prompt for this configuration choice.

## Understand ownership and security

Device-reported values can be inaccurate or deliberately spoofed by a compromised device. A mapping controls which reported values become labels, but it does not attest that the reported values are true. Use stable attributes and review selectors carefully before using them to target fleets that receive sensitive configuration or credentials.

Flight Control tracks ownership for each mapped label key on each device. An operator or device cannot change or remove a label while a mapping currently owns that exact key; such a device update returns HTTP 409. The server-computed `device-controller/managedLabels` annotation lists currently owned keys. Treat this annotation as read-only.

A mapping can take ownership of an existing user label with the same key. The mapping replaces the existing value, and deleting the mapping does not restore the former value. Use dedicated label keys to avoid collisions. If two mappings produce the same key on a device, the affected mappings report a collision and their previous outputs are retained until the conflict is resolved.

Use provenance to inspect current ownership. Device provenance returns currently mapped keys and their mapping owners without returning label values. Organization provenance resolves ownership for exact keys, including scalar keys that are reserved by a mapping but do not yet have a device value:

| Query | Method and path |
|---|---|
| Current mapping-owned keys for a device | `GET /api/v1/devices/{name}/labelsyncprovenance` |
| Current owners for exact keys in an organization | `GET /api/v1/labelsyncprovenance?labelKeys=<key>` |

Provenance describes current ownership. Device events provide the history of label changes.

## Monitor propagation

Mapping updates and device status updates are applied asynchronously. A mapping's `Ready` condition reports its propagation state:

| Reason | Meaning |
|---|---|
| `Pending` | The mapping has changed and propagation is waiting to run. |
| `Success` | The current mapping generation has propagated across the scan. |
| `Degraded` | One or more devices could not be reconciled. Check the condition message and correct the expression or output collision. |

Device creation and status updates request reconciliation for that device. A bounded periodic scan also propagates mapping changes and retries devices that did not reconcile successfully. With the default Helm values, the scan runs every two minutes, processes up to 1,000 devices per page, and spends up to 30 seconds in one invocation. If a scan needs more time, it resumes on a later run.

An expression error, invalid output key, output collision, or output limit failure sets the mapping to `Degraded`. The reconciler retains the mapping's last successful labels while that mapping is failing. Correct the mapping or reported input, then monitor the Ready condition for `Success`.

Deleting a mapping starts cleanup of the labels it owns. Cleanup is asynchronous; the mapping resource can remain until owned labels have been removed.

To review label changes over time, inspect device events. To see which mapping currently owns a key, use the provenance API described above.

## Configure initial mappings

Deployments provide an initial set of mappings for organizations. These mappings are ordinary resources after creation, so operators can update or delete them through the REST API.

### Helm deployments

Set `organizations.initialLabelSyncMappings` in the Helm values file to replace the bundled set with your own sequence of `LabelSyncMapping` resources. Set it to an empty sequence to disable automatic mapping creation:

```yaml
organizations:
  initialLabelSyncMappings: []
```

The Helm chart makes this manifest available to the services and database migration through read-only ConfigMaps. To keep a bundled mapping and add another, include the full bundled sequence and your additional resource in the values file.

### Quadlet deployments

Quadlet deployments use `/etc/flightctl/label-sync/mappings.yaml` as the initial mapping manifest. The deployment installs the packaged defaults only when this file does not exist. It preserves an existing file and mounts its directory read-only into the services that provision organizations.

Edit the host manifest to change the defaults used for future organization provisioning. The deployment does not replace the file during a later deployment.

### Update existing organizations

The manifest is used when Flight Control creates an organization and during a one-time migration for organizations that already exist. Provisioning attempts to create each mapping only if its name is not already present. It does not continuously reconcile the manifest with organization resources. Later edits to Helm values or the quadlet manifest do not update mappings that already exist.

To change a mapping in an existing organization, update that organization's `LabelSyncMapping` resource through the REST API. Changes propagate asynchronously and appear in the mapping's Ready condition. Provisioning errors are logged and do not stop organization creation or database migration; create any missing mappings through the API.

See [Installing the Flight Control Service on Kubernetes](../installing/installing-service-on-kubernetes.md) and [Installing the Flight Control Service on Linux](../installing/installing-service-on-linux.md) for deployment instructions.

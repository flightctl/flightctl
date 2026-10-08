# Managing dynamic label mappings

Dynamic label mappings promote selected device-reported values into device labels. Use these labels to select fleets and rollout batches, render fleet templates, and filter device inventories.

> [!WARNING]
> Device-promoted labels are inherently riskier than operator-assigned labels because their values come from the device and are not attested. A compromised device can report a value that matches a fleet selector and receive that fleet's configuration. Do not use promoted labels as proof of device identity or integrity. Prefer operator-assigned labels when selecting fleets that receive sensitive configuration or credentials.

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
    status.systemInfo.customInfo.transformMapEntry(
      k,
      v,
      {"ops.example.com/customInfo." + k: v}
    )
```

This map expression is a demonstration that assumes `customInfo` exists and its entries can produce valid label keys and scalar values. In production, account for missing fields and input values that could produce invalid labels.

This scalar mapping combines the reported distribution ID and version into one label value, such as `rhel-9.5`:

```yaml
apiVersion: flightctl.io/v1beta1
kind: LabelSyncMapping
metadata:
  name: os-release
spec:
  resourceType: Device
  key: ops.example.com/os-release
  expression: |
    has(status.systemInfo.distroId) && has(status.systemInfo.distroVersion)
      ? status.systemInfo.distroId + "-" + status.systemInfo.distroVersion
      : dyn(null)
```

In a map expression, `transformMapEntry(k, v, result)` evaluates `result` for each source key and value. The expression can return different destination keys. You can add a Boolean condition before `result` to filter entries. The result must use complete label keys.

## CEL syntax and extensions

Mapping expressions use [CEL](https://cel.dev/overview/cel-overview) over the device's `metadata`, `spec`, and `status`. The evaluator enables these extensions:

| Extension | Available syntax | Description |
|---|---|---|
| [Optional types](https://pkg.go.dev/cel.dev/cel-go/cel#OptionalTypes) | `optional.none()`, `optional.of(value)`, `.?field`, `orValue(value)` | Represents a value that may be absent and lets an expression handle that absence. |
| [Two-variable comprehensions](https://pkg.go.dev/cel.dev/cel-go/ext#TwoVarComprehensions) | `transformList`, `transformMap`, `transformMapEntry` | Iterates a list or map with an index/key variable and a value variable. `transformMapEntry` can produce new label keys from map entries. |
| Flight Control semantic-version functions | `isSemver(value)`, `semver(value)`, `compareTo(value)` | Validates and compares semantic-version strings. |

The CEL-Go extensions above are the optional-types and two-variable-comprehension extensions. CEL's standard operators and functions, such as `has()` and `size()`, are also available. Other CEL-Go extension libraries are not enabled for mapping expressions.

The Flight Control semantic-version library provides three functions:

| Function | Result |
|---|---|
| `isSemver(value)` | Returns `true` if the string can be parsed as a semantic version. |
| `semver(value)` | Parses a string as a semantic version for comparison. |
| `version.compareTo(other)` | Returns a negative number, zero, or a positive number when `version` precedes, equals, or follows `other`. |

Before parsing, the function removes spaces at the start and ignores any prefix before the first number. For example, it accepts `v1.2.3`. It compares versions by [semantic version rules](https://semver.org/), not as plain text. This expression is true when the reported agent version is `1.2.3` or newer:

```cel
isSemver(status.systemInfo.agentVersion) &&
  semver(status.systemInfo.agentVersion).compareTo(semver("1.2.3")) >= 0
```

CEL expressions can use optional values. Use `has()` or a conditional expression to handle fields that might be absent. An absent field, a null result, an empty result, or `optional.none()` produces no label value for that device. A successful mapping with no value removes a previous label owned by that mapping.

Mapping results must be a scalar value or a map of scalar values. Nested maps, lists, and other complex results are rejected. Scalar values are converted to strings and sanitized for use as label values. Map keys must be valid label keys.

Each map expression can return up to 50 entries. A device can have up to 100 mapping-owned labels across all mappings. An expression or output that exceeds these limits is reported as a mapping failure.

## Manage mappings from the CLI and API

Manage `LabelSyncMapping` resources with the CLI or REST API. For API resource details, see [LabelSyncMappings](../references/api-resources.md#labelsyncmappings).

## Use mapped labels

Promoted labels use the same selectors as other device labels. This fleet selector targets devices whose reported region is east:

> [!IMPORTANT]
> Always choose stable, predictable values for fleet selectors that use promoted labels. A changing reported value can move a device into or out of a fleet and trigger a configuration rollout.

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

Deployments provide a starter set of mappings for organizations. After provisioning, mappings are ordinary resources that you can manage with the CLI or REST API.

### Helm deployments

Set `organizations.initialLabelSyncMappings` in the Helm values file to supply your own mappings for organization provisioning. Set it to an empty sequence to disable automatic mapping creation:

```yaml
organizations:
  initialLabelSyncMappings: []
```

### Quadlet deployments

Quadlet deployments use `/etc/flightctl/label-sync/mappings.yaml` as the initial mapping manifest. Edit this file to change the mappings used for future organization provisioning.

### Update existing organizations

Changes to Helm values or the quadlet manifest affect mappings used for future organization provisioning. To change mappings in an existing organization, update its `LabelSyncMapping` resources with the CLI or REST API. Changes propagate asynchronously; see [Monitor propagation](#monitor-propagation).

See [Installing the Flight Control Service on Kubernetes](../installing/installing-service-on-kubernetes.md) and [Installing the Flight Control Service on Linux](../installing/installing-service-on-linux.md) for deployment instructions.

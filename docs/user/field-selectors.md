# Field Selectors

Field selectors filter a list of Flight Control resources based on specific resource field values.
They follow the same syntax, principles, and operators as Kubernetes Field and Label selectors, with additional operators available for more advanced search use cases.

## Supported Fields

Flight Control resources provide a set of metadata fields that can be selected.

Each resource supports the following metadata fields:

- `metadata.name`
- `metadata.owner`
- `metadata.creationTimestamp`

> [!NOTE]
> To query labels, use Label Selectors for advanced and flexible label filtering.

### List of Additional Supported Fields

In addition to the metadata fields, each resource has its own unique set of fields that can be selected, offering further flexibility in filtering and selection based on resource-specific attributes.

The following table lists the fields supported for filtering for each resource kind:

| Kind                            | Fields                                              |
|---------------------------------|-----------------------------------------------------|
| **Certificate Signing Request** | `status.certificate`                                |
| **Device**                      | `status.summary.status`<br/>`status.applicationsSummary.status`<br/>`status.updated.status`<br/>`lastSeen`<br/>`status.lifecycle.status` |
| **Enrollment Request**          | `status.approval.approved`<br/>`status.certificate` |
| **Fleet**                       | `spec.template.spec.os.image`                       |
| **Repository**                  | `spec.type`<br/>`spec.url`                          |
| **Resource Sync**               | `spec.repository`                                   |

### Examples

#### Example 1: Excluding a Specific Device by Name

The following command filters out a specific device by its name:

```bash
flightctl get devices --field-selector 'metadata.name!=c3tkb18x9fw32fzx5l556n0p0dracwbl4uiojxu19g2'
```

#### Example 2: Filter by Owner, Labels, and Creation Timestamp

This command retrieves devices owned by `Fleet/pos-fleet`, located in the `us` region, and created in 2024:

```bash
flightctl get devices --field-selector 'metadata.owner=Fleet/pos-fleet, metadata.creationTimestamp >= 2024-01-01T00:00:00Z, metadata.creationTimestamp < 2025-01-01T00:00:00Z' -l 'region=us'
```

#### Example 3: Filter by Owner, Labels, and Device Status

This command retrieves devices owned by `Fleet/pos-fleet`, located in the `us` region, and with a `status.updated.status` of either `Unknown` or `OutOfDate`:

```bash
flightctl get devices --field-selector 'metadata.owner=Fleet/pos-fleet, status.updated.status in (Unknown, OutOfDate)' -l 'region=us'

```

### Fields Discovery

Some Flight Control resources might expose additional supported fields. You can discover the supported fields by using `flightctl` with the `--field-selector` option. If you attempt to use an unsupported field, the error message will list the available supported fields.

For example:

```bash
flightctl get device --field-selector='text'

Error: listing devices: 400, message: unknown or unsupported selector: unable to resolve selector name "text". Supported selectors are: [metadata.alias metadata.creationTimestamp metadata.name metadata.nameOrAlias metadata.owner status.applicationsSummary.status lastSeen status.summary.status status.updated.status]
```

In this example, the field `text` is not a valid field for filtering. The error message provides a list of supported fields that can be used with `--field-selector` for the `device` resource.

You can then use one of the supported fields, as shown below:

```bash
flightctl get devices --field-selector 'metadata.alias contains cluster'
```

In this command, the `metadata.alias` field is checked with the containment operator `contains` to see if it contains the value `cluster`.

## Supported operators

| Operator             | Symbol         | Description                                           |
|----------------------|----------------|-------------------------------------------------------|
| Exists               | `<field>` | Checks if a field exists. For example, the `--field-selector 'metadata.owner'` field selector returns resources that have the `metadata.owner` field.                        |
| DoesNotExist         | `!`            | Checks if a field does not exist                      |
| Equals               | `=`            | Checks if a field is equal to a value                 |
| DoubleEquals         | `==`           | Another form of equality check                        |
| NotEquals            | `!=`           | Checks if a field is not equal to a value             |
| GreaterThan          | `>`            | Checks if a field is greater than a value             |
| GreaterThanOrEquals  | `>=`           | Checks if a field is greater than or equal to a value |
| LessThan             | `<`            | Checks if a field is less than a value                |
| LessThanOrEquals     | `<=`           | Checks if a field is less than or equal to a value    |
| In                   | `in`           | Checks if a field is within a list of values          |
| NotIn                | `notin`        | Checks if a field is not in a list of values          |
| Contains             | `contains`     | Checks if a field contains a value                    |
| NotContains          | `notcontains`  | Checks if a field does not contain a value            |

### Operators Usage by Field Type

Each field type supports a specific subset of operators, listed below:

| Field Type | Supported Operators                                                                                      | Value                                 |
|------------|----------------------------------------------------------------------------------------------------------|--------------------------------------|
| **String** | `Equals`: Matches if the field value is an exact match to the specified string.<br> `DoubleEquals`: Matches if the field value is an exact match to the specified string (alternative to `Equals`).<br> `NotEquals`: Matches if the field value is not an exact match to the specified string.<br> `In`: Matches if the field value matches at least one string in the list.<br> `NotIn`: Matches if the field value does not match any of the strings in the list.<br> `Contains`: Matches if the field value contains the specified substring.<br> `NotContains`: Matches if the field value does not contain the specified substring.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Text string                       |
| **Timestamp**    | `Equals`: Matches if the field value is an exact match to the specified timestamp.<br> `DoubleEquals`: Matches if the field value is an exact match to the specified timestamp (alternative to `Equals`).<br> `NotEquals`: Matches if the field value is not an exact match to the specified timestamp.<br> `GreaterThan`: Matches if the field value is after the specified timestamp.<br> `GreaterThanOrEquals`: Matches if the field value is after or equal to the specified timestamp.<br> `LessThan`: Matches if the field value is before the specified timestamp.<br> `LessThanOrEquals`: Matches if the field value is before or equal to the specified timestamp.<br> `In`: Matches if the field value matches at least one timestamp in the list.<br> `NotIn`: Matches if the field value does not match any of the timestamps in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | RFC 3339 format           |
| **Number**    | `Equals`: Matches if the field value equals the specified number.<br> `DoubleEquals`: Matches if the field value equals the specified number (alternative to `Equals`).<br> `NotEquals`: Matches if the field value does not equal to the specified number.<br> `GreaterThan`: Matches if the field value is greater than the specified number.<br> `GreaterThanOrEquals`: Matches if the field value is greater than or equal to the specified number.<br> `LessThan`: Matches if the field value is less than the specified number.<br> `LessThanOrEquals`: Matches if the field value is less than or equal to the specified number.<br> `In`: Matches if the field value equals at least one number in the list.<br> `NotIn`: Matches if the field value does not equal any numbers in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Number format |
| **Boolean**      | `Equals`: Matches if the value is `true` or `false`.<br> `DoubleEquals`: Matches if the value is `true` or `false` (alternative to `Equals`).<br> `NotEquals`: Matches if the value is the opposite of the specified value.<br> `In`: Matches if the value (`true` or `false`) is in the list.<br><i>**Note:** The list can only contain `true` or `false`, so this operator is limited in use.</i><br> `NotIn`: Matches if the value is not in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Boolean format (`true`, `false`)                 |
| **Array**        | `Contains`: Matches if the array contains the specified value.<br> `NotContains`: Matches if the array does not contain the specified value.<br> `In`: Matches if the array overlaps with the specified values.<br> `NotIn`: Matches if the array does not overlap with the specified values.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Array element |

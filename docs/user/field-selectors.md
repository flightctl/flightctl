# Field Selectors

Field selectors filter a list of Flight Control objects based on specific resource field values.<br>
They follow the same syntax, principles, and support the same operators as Kubernetes Field and Label selectors, with additional operators available for more advanced search use cases.


## Supported operators

| Operator             | Symbol | Description                                 |
|----------------------|--------|---------------------------------------------|
| Exists               | `exists` | Checks if a field exists                   |
| DoesNotExist         | `!`      | Checks if a field does not exist           |
| Equals               | `=`      | Checks if a field is equal to a value      |
| DoubleEquals         | `==`     | Another form of equality check             |
| NotEquals            | `!=`     | Checks if a field is not equal to a value  |
| GreaterThan          | `>`      | Checks if a field is greater than a value  |
| GreaterThanOrEquals  | `>=`     | Checks if a field is greater than or equal to a value |
| LessThan             | `<`      | Checks if a field is less than a value     |
| LessThanOrEquals     | `<=`     | Checks if a field is less than or equal to a value |
| In                   | `in`     | Checks if a field is within a list of values |
| NotIn                | `notin`  | Checks if a field is not in a list of values |
| Contains             | `@>`     | Checks if a field contains a value         |
| NotContains          | `!@`     | Checks if a field does not contain a value |


### Operators Usage by Field Type

Each field type supports a specific subset of operators, listed below:


| **Field Type** | **Supported Operators**                                                                                      | **Value**                                 |
|----------------|---------------------------------------------------------------------------------------------------------------|-------------------------------------------|
| **String** | `Equals`: Matches if the field value is an exact match to the specified string.<br> `DoubleEquals`: Matches if the field value is an exact match to the specified string (alternative to `Equals`).<br> `NotEquals`: Matches if the field value is not an exact match to the specified string.<br> `In`: Matches if the field value matches at least one string in the list.<br> `NotIn`: Matches if the field value does not match any of the strings in the list.<br> `Contains`: Matches if the field value contains the specified substring.<br> `NotContains`: Matches if the field value does not contain the specified substring.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Text string                       |
| **Timestamp**    | `Equals`: Matches if the field value is an exact match to the specified timestamp.<br> `DoubleEquals`: Matches if the field value is an exact match to the specified timestamp (alternative to `Equals`).<br> `NotEquals`: Matches if the field value is not an exact match to the specified timestamp.<br> `GreaterThan`: Matches if the field value is after the specified timestamp.<br> `GreaterThanOrEquals`: Matches if the field value is after or equal to the specified timestamp.<br> `LessThan`: Matches if the field value is before the specified timestamp.<br> `LessThanOrEquals`: Matches if the field value is before or equal to the specified timestamp.<br> `In`: Matches if the field value matches at least one timestamp in the list.<br> `NotIn`: Matches if the field value does not match any of the timestamps in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | RFC3339 format           |
| **Number**    | `Equals`: Matches if the field value equals the specified number.<br> `DoubleEquals`: Matches if the field value equals the specified number (alternative to `Equals`).<br> `NotEquals`: Matches if the field value does not equal to the specified number.<br> `GreaterThan`: Matches if the field value is greater than the specified number.<br> `GreaterThanOrEquals`: Matches if the field value is greater than or equal to the specified number.<br> `LessThan`: Matches if the field value is less than the specified number.<br> `LessThanOrEquals`: Matches if the field value is less than or equal to the specified number.<br> `In`: Matches if the field value equals at least one number in the list.<br> `NotIn`: Matches if the field value does not equal any numbers in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Number format           |            |
| **Boolean**      | `Equals`: Matches if the value is `true` or `false`.<br> `DoubleEquals`: Matches if the value is `true` or `false` (alternative to `Equals`).<br> `NotEquals`: Matches if the value is the opposite of the specified value.<br> `In`: Matches if the value (`true` or `false`) is in the list.<br><i>**Note:** The list can only contain `true` or `false`, so this operator is limited in use.</i><br> `NotIn`: Matches if the value is not in the list.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present. | Boolean format (`true`, `false`)                 |
| **Array**        | `Contains`: Matches if the array contains the specified value.<br> `NotContains`: Matches if the array does not contain the specified value.<br> `In`: Matches if the array overlaps with the specified values.<br> `NotIn`: Matches if the array does not overlap with the specified values.<br> `Exists`: Matches if the field is present.<br> `DoesNotExist`: Matches if the field is not present.<br><br><i>**Note:** Using `Array[Index]` treats the element as the type defined for the array elements (e.g., string, timestamp, number, boolean).</i> | Array element    |
| **JSON**        | `Equals`: Matches if the value at the specified key path is an exact match to the provided JSON value.<br> `DoubleEquals`: Matches if the value at the specified key path is an exact match to the provided JSON value (alternative to `Equals`).<br> `NotEquals`: Matches if the value at the specified key path does not match the provided JSON value. If the key path does not exist, it is considered a match.<br> `In`: Matches if the value at the specified key path matches at least one JSON value in the provided list.<br> `NotIn`: Matches if the value at the specified key path does not match any of the JSON values in the list. If the key path does not exist, it is considered a match.<br> `Exists`: Matches if the specified key path is present in the JSON field.<br> `DoesNotExist`: Matches if the specified key path is not present in the JSON field.<br> `Contains`: Matches if the value at the specified key path contains the provided JSON value.<br> `NotContains`: Matches if the value at the specified key path does not contain the provided JSON value. | JSON   |


## Supported Fields

The following table lists the fields supported for filtering Flight Control objects:

| Field Name                   | Field Type           |
|------------------------------|----------------------|
| `metadata.name`              | `String`             |
| `metadata.owner`             | `String`             |
| `metadata.labels`            | `StringArray`        |
| `metadata.annotations`       | `StringArray`        |
| `metadata.creationTimestamp` | `Timestamp`          |
| `metadata.deletionTimestamp` | `Timestamp`          |
| `spec`                       | `JSON`               |
| `status`                     | `JSON`               |

> **Note:** While `metadata.labels` can be queried using Field Selectors, for more extensive label querying options, consider using Label Selectors.

### Usage Examples

Use the `--field-selector` flag to filter devices based on field values. Here are some examples:

#### Example 1: Exclude a Specific Device by Name
This command filters out a specific device by its name:
```bash
flightctl get devices --field-selector 'metadata.name!=c3tkb18x9fw32fzx5l556n0p0dracwbl4uiojxu19g2'
```
#### Example 2: Filter by Owner, Labels, and Creation Timestamp
This command retrieves devices owned by `Fleet/pos-fleet`, located in the `us` region, and created in 2024:
```bash
flightctl get devices --field-selector 'metadata.owner=Fleet/pos-fleet, metadata.labels @> region\=us, metadata.creationTimestamp >= 2024-01-01T00:00:00Z, metadata.creationTimestamp < 2025-01-01T00:00:00Z'
```
#### Example 3: Filter by Owner, Labels, and Status
This command retrieves devices owned by `Fleet/pos-fleet`, located in the `us` region, and with a `status.updated.status` of either "Unknown" or "OutOfDate":
```bash
flightctl get devices --field-selector 'metadata.owner=Fleet/pos-fleet, metadata.labels @> region\=us, status.updated.status in ("Unknown", "OutOfDate")'
```

### Fields Discovery

Some Flight Control Objects might expose additional supported fields. You can discover the supported fields by using `flightctl` with the `--field-selector` option. If you attempt to use an unsupported field, the error message will list the available supported fields.

For example:

```bash
flightctl get device --field-selector='text'

Error: listing devices: 400, message: unknown or unsupported field: unable to resolve field name "text". Supported fields are: [metadata.annotations metadata.creationTimestamp metadata.deletionTimestamp spec metadata.owner metadata.labels metadata.alias status metadata.name metadata.nameoralias]
```
In this example, the field `text` is not a valid field for filtering. The error message provides a list of supported fields that can be used with `--field-selector` for the `device` object.

You can then use one of the supported fields, as shown below:
```bash
flightctl get devices --field-selector 'metadata.alias @> cluster'
```
In this command, the `metadata.alias` field is checked with the containment operator `@>` to see if it contains the value `cluster`.

### Handling JSON Fields

JSON fields allow querying by specific JSON values, but certain scenarios may require additional functionality.
<br>For example:

- Casting is available to treat values as specific data types, such as numbers, enabling comparison operators.
- The containment operator allows checking if a JSON field or array includes particular values.

These features provide greater flexibility for querying JSON data.


#### Casting

Casting enables you to treat the value at the specified key path as another supported datatype, allowing the use of relevant operators for that type. 

Supported casting types are: `boolean`, `integer`, `smallint`, `bigint`, `float`, `timestamp`, and `string`.

To use casting, add the `::<TYPE>` suffix to the key path when filtering.


For example:
```bash
flightctl get devices --field-selector 'status.lastSeen::timestamp < 2024-11-07T00:00:00Z'
```
In this example, the `status.lastSeen` field is cast to `timestamp`, allowing the use of the `<` operator with an RFC3339-formatted date. This retrieves devices where `status.lastSeen` is earlier than the specified date.

#### Containment Operator

The containment operator `@>` matches if the value at the specified key path contains the provided JSON value.

This operator can also handle JSON arrays and can be used to check if a JSON array contains specific JSON path/value entries at the top level.

For example:
```bash
flightctl get devices --field-selector 'spec.applications @> [{"image":"quay.io/rh_ee_camadorg/oci-app"}]'
```
In this example, the `spec.applications` field is checked to see if it contains an entry with `"image": "quay.io/rh_ee_camadorg/oci-app"`. This retrieves devices where at least one element in the `spec.applications` array has an `image` field with this specific value.



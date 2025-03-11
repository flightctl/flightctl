# Field Selectors

Field selectors filter a list of Flight Control resources based on specific resource field values.
They follow the same syntax, principles, and operators as Kubernetes Field and Label selectors, with additional operators available for more advanced search use cases.

## Adding Selectors

Selectors are always tied to a resource model (e.g., device, fleet, etc.).

Inside `internal/store/model`, each resource model can define selectors. Once passed to `NewFieldSelector(dest any)`, the selectors will be detected and resolved automatically.

There are three ways to define a resolvable selector:

### 1. Tagging a Resource Model Field

You can tag a resource model field with a selector name:
```go
Name string `gorm:"primary_key;" selector:"metadata.name"`
```
Once the field is tagged with a selector name, it can be resolved using the field-selector corresponding to that name. The field name (or DBName) and type will be automatically detected by the field-selector.

#### Using Hidden and Private Selectors
Selectors can be further annotated as hidden or private to control their behavior:
```go
Labels JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.labels,hidden,private"`

```
* `hidden`: The selector will not be exposed to end-users during discovery. It will not appear in the list of available selectors.

* `private`: The selector cannot be directly used by the field-selector. However, it may still be utilized internally by other selectors, such as the label selector.

### 2. Adding Selector Mapping

A resource model can define a resolvable selector name mapped to an existing selector defined by the resource model.

To enable this, the resource model must implement the following functions, which will be used by the field-selector during resolution:

```go
MapSelectorName(name selector.SelectorName) []selector.SelectorName

ListSelectors() selector.SelectorNameSet
```

This is useful in cases where:
- You rename a selector and want to support the deprecated name for backward compatibility.
- You want to map one selector name to multiple existing selectors (e.g., nameOrAlias).

#### Example: Add mapping to metadata.nameOrAlias selector
```go
func (m *Device) MapSelectorName(name selector.SelectorName) []selector.SelectorName {
	if strings.EqualFold("metadata.nameOrAlias", name.String()) {
		return []selector.SelectorName{
			selector.NewSelectorName("metadata.name"),
			selector.NewSelectorName("metadata.alias"),
		}
	}
	return nil
}

func (m *Device) ListSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(selector.NewSelectorName("metadata.nameOrAlias"))
}
```

> [!NOTE]
> - Mapping to multiple selectors will result in an OR condition between them.
> - A mapped selector will be resolved first. It can override or hide an existing resolver defined by the resource model.
> - `NewSelectorName` can be replaced with `NewHiddenSelectorName` to mark the selector as hidden. This ensures it won't be exposed during discovery but can still be used internally (see the explanation above).

### 3. Custom Selector Resolution

For more advanced cases, selectors can be manually resolved.

To enable this, the resource model must implement the following functions, which will be used by the field-selector during resolution:

```go
ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) 

ListSelectors() selector.SelectorNameSet
```

#### Example Use Case: Whitelisting

We use custom selectors to create a whitelist for the `Status` and `Spec` fields.
For all Flight Control resources, `Status` and `Spec` fields are of type **JSONB**.
Using explicit tagging on the resource model field results in allowing all **JSONB** keys to be queried.

For example, tagging the `Status` field to allow it to be resolved:
```go
// The last reported state, stored as opaque JSON object.
Status *JSONField[api.DeviceStatus] `gorm:"type:jsonb" selector:"metadata.status"`
```

The field-selector, in turn, resolves this field as type JSONB, automatically allowing keys like `status.updated.status` to be queried.


While this approach is powerful, it introduces two challenges:

- Type Constraints:
Resolved keys only accept JSON values. For example:
`status.updated.status="UpToDate"`

- Loss of Control:
Allowing all `Status` keys to be queried restricts our ability to whitelist and only support specific keys.

#### Custom Selector for JSONB Fields

Adding Custom Selector for `status.updated.status`
```go
func (m *Device) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
  if strings.EqualFold("status.updated.status", name.String()) {
		return &selector.SelectorField{
			Type:      selector.String,
			FieldName: "status->'updated'->>'status'",
			FieldType: "jsonb",
		}, nil
	}
	return nil, nil
}

func (m *Device) ListSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add("status.updated.status")
}
```

> [!NOTE]
> - Mapping a selector name using `MapSelectorName` to a custom selector is also supported. This can be useful for deprecating a key.
> - When `FieldType` is defined as **JSONB**, the field-selector Adapts the `FieldName` to a valid **JSONB** key (e.g., `status -> 'updated' ->> 'status'`).
> - If the `Type` is different from **JSONB**, the field-selector will cast the key to the corresponding type, and the selector will be processed as that type.
> - A custom selector is resolved after mapped selectors. It can override or hide an existing resolver defined by the resource model.


## Kubernetes Selector Package

The field-selector uses the Kubernetes selector to initially parse the query.

The package was added in `/pkg/k8s/selector`, and the following modifications were made:

- Kubernetes label selectors are now used for both fields and labels.
- The functions `validateLabelKey` and `validateLabelValue` are applied only to labels.
- Fields have a modified lexer to support escaping and RHS symbols (e.g., `key in (x=y)`).
- Two new operators, `contains` and `notcontains`, were added.

A "vanilla" commit of Kubernetes selector: `86edb2182817bb492751786aa8471732abadf8ab`

package selector

import (
	"fmt"
	"strings"
	"sync"

	gormschema "gorm.io/gorm/schema"
)

var (
	cacheStore = &sync.Map{}
)

// SelectorFieldName represents the name of a field used in a selector.
type SelectorFieldName string

// FieldNameResolver defines an interface that allows for resolving more complex field name mappings.
// This can be useful when certain fields map to other names or when dealing with complex schemas.
type FieldNameResolver interface {
	ResolveFieldName(field SelectorFieldName) []SelectorFieldName
}

// selectorFieldResolver is a struct that provides the ability to resolve selector fields to
// their corresponding schema fields. It holds a map of schema fields and optionally a field
// resolver for advanced cases.
type selectorFieldResolver struct {
	schemaFields  map[SelectorFieldName]*gormschema.Field
	fieldResolver FieldNameResolver
}

// SelectorFieldResolver initializes a new selectorFieldResolver. It resolves schema fields from the provided model.
// If the model implements FieldNameResolver, it will be used to resolve custom field names.
func SelectorFieldResolver(model any) (*selectorFieldResolver, error) {
	resolved, err := ResolveFieldsFromSchema(model)
	if err != nil {
		return nil, err
	}

	fr := &selectorFieldResolver{schemaFields: resolved}
	if resolver, ok := model.(FieldNameResolver); ok {
		fr.fieldResolver = resolver
	}
	return fr, nil
}

// ResolveNames returns a map of resolved field names to their database representations.
// It maps selector fields to the database field names, handling JSONB data types accordingly.
func (sr *selectorFieldResolver) ResolveNames(field SelectorFieldName) (map[SelectorFieldName]string, error) {
	resolvedFields, err := sr.ResolveFields(field)
	if err != nil {
		return nil, err
	}

	fields := make(map[SelectorFieldName]string, len(resolvedFields))
	for selectorField, schemaField := range resolvedFields {
		switch schemaField.DataType {
		case "jsonb":
			fields[selectorField] = string(selectorField)
		default:
			fields[selectorField] = schemaField.DBName
		}
	}
	return fields, nil
}

// ResolveFields resolves a selector field name to its corresponding GORM schema fields.
// It also supports resolving JSONB fields and custom field resolutions if a fieldResolver is present.
func (sr *selectorFieldResolver) ResolveFields(field SelectorFieldName) (map[SelectorFieldName]*gormschema.Field, error) {
	resolve := func(fn SelectorFieldName) *gormschema.Field {
		if resolvedField, exists := sr.schemaFields[fn]; exists {
			return resolvedField
		}

		// Handle JSONB fields
		fieldParts := strings.Split(string(fn), ".")
		if len(fieldParts) > 1 {
			baseField := fieldParts[0]
			for _, schemaField := range sr.schemaFields {
				if schemaField.DataType == "jsonb" && schemaField.DBName == baseField {
					return schemaField
				}
			}
		}
		return nil
	}

	resolvedField := resolve(field)
	if resolvedField != nil {
		return map[SelectorFieldName]*gormschema.Field{field: resolvedField}, nil
	}

	if sr.fieldResolver != nil {
		refs := sr.fieldResolver.ResolveFieldName(field)
		if len(refs) > 0 {
			fields := make(map[SelectorFieldName]*gormschema.Field, len(refs))
			for _, ref := range refs {
				resolvedField := resolve(ref)
				if resolvedField == nil {
					return nil, fmt.Errorf("unable to resolve field name %q", ref)
				}
				fields[ref] = resolvedField
			}
			return fields, nil
		}
	}

	return nil, fmt.Errorf("unknown field name %q", field)
}

// ResolveFieldsFromSchema parses the schema of the given model and extracts the fields annotated with
// the `selector` tag. This is useful for determining which fields can be used in selector queries.
func ResolveFieldsFromSchema(dest any) (map[SelectorFieldName]*gormschema.Field, error) {
	schema, err := gormschema.ParseWithSpecialTableName(dest, cacheStore, gormschema.NamingStrategy{IdentifierMaxLength: 64}, "")
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}

	fieldMap := make(map[SelectorFieldName]*gormschema.Field)
	for _, field := range schema.Fields {
		if selector := field.StructField.Tag.Get("selector"); selector != "" {
			fieldMap[SelectorFieldName(selector)] = field
		}
	}
	return fieldMap, nil
}

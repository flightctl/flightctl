package selector

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/flterrors"
	gormschema "gorm.io/gorm/schema"
)

var (
	cacheStore           = &sync.Map{}
	schemaTypeResolution = map[gormschema.DataType]SelectorFieldType{
		gormschema.Bool:   Bool,
		gormschema.Int:    Int,
		gormschema.Float:  Float,
		gormschema.String: String,
		gormschema.Time:   Time,
		"boolean[]":       BoolArray,
		"integer[]":       IntArray,
		"smallint[]":      SmallIntArray,
		"bigint[]":        BigIntArray,
		"real[]":          FloatArray,
		"text[]":          TextArray,
		"timestamp[]":     TimestampArray,
		"jsonb":           Jsonb,
	}

	castTypeResolution = map[string]SelectorFieldType{
		"boolean":     Bool,
		"integer":     Int,
		"smallint":    SmallInt,
		"bigInt":      BigInt,
		"float":       Float,
		"timestamp":   Time,
		"boolean[]":   BoolArray,
		"integer[]":   IntArray,
		"smallint[]":  SmallIntArray,
		"bigint[]":    BigIntArray,
		"real[]":      FloatArray,
		"text[]":      TextArray,
		"timestamp[]": TimestampArray,
		"text":        String,
		"string":      String,
	}
)

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

// ResolveNames returns a slice of resolved field names to their database representations.
// It maps selector fields to the database field names, handling JSONB data types accordingly.
func (sr *selectorFieldResolver) ResolveNames(field SelectorFieldName) ([]string, error) {
	resolvedFields, err := sr.ResolveFields(field)
	if err != nil {
		return nil, err
	}

	fields := make([]string, 0, len(resolvedFields))
	for _, selectorField := range resolvedFields {
		fields = append(fields, selectorField.DBName)
	}
	return fields, nil
}

// ResolveFields resolves a selector field name to its corresponding GORM schema fields.
// It also supports resolving JSONB fields and custom field resolutions if a fieldResolver is present.
func (sr *selectorFieldResolver) ResolveFields(field SelectorFieldName) ([]*SelectorField, error) {
	resolve := func(fn SelectorFieldName) (*SelectorField, error) {
		if resolvedField, exists := sr.schemaFields[fn]; exists {
			fieldType, ok := schemaTypeResolution[resolvedField.DataType]
			if !ok {
				return nil, fmt.Errorf("unknown or unsupported schema type for field: %s", fn)
			}

			if fieldType.IsArray() {
				fieldKind := resolvedField.StructField.Type.Kind()
				if fieldKind != reflect.Array && fieldKind != reflect.Slice {
					return nil, fmt.Errorf("field %s is expected to be an array or slice, but got %s", fn, fieldKind.String())
				}
			}

			return &SelectorField{
				DBName:      resolvedField.DBName,
				Type:        fieldType,
				DataType:    resolvedField.DataType,
				StructField: resolvedField.StructField,
			}, nil
		}

		// Handle JSONB fields
		jsonbField := strings.TrimSpace(string(fn))
		fieldParts := strings.Split(jsonbField, ".")
		if len(fieldParts) > 1 {
			// Iterate through schema fields to find the matching JSONB field
			for _, schemaField := range sr.schemaFields {
				if schemaField.DataType == "jsonb" && schemaField.DBName == fieldParts[0] {
					// Check if there's a "::" in the JSONB field name
					if strings.Contains(jsonbField, "::") {
						parts := strings.Split(jsonbField, "::")
						if len(parts) != 2 {
							return nil, fmt.Errorf("invalid jsonb field format: %s", jsonbField)
						}
						fieldName := parts[0]
						suffix := parts[1]

						// Check if the suffix exists in the type resolutions map
						if fieldType, ok := castTypeResolution[suffix]; ok {
							return &SelectorField{
								DBName:      fieldName,
								Type:        fieldType,
								DataType:    schemaField.DataType,
								StructField: schemaField.StructField,
							}, nil
						} else {
							return nil, fmt.Errorf("unknown or unsupported suffix in jsonb field: %s", suffix)
						}
					}

					// Original logic if no "::" is present
					return &SelectorField{
						DBName:      string(fn),
						Type:        Jsonb,
						DataType:    schemaField.DataType,
						StructField: schemaField.StructField,
					}, nil
				}
			}
		}
		return nil, nil
	}

	resolvedField, err := resolve(field)
	if err != nil {
		return nil, NewSelectorError(flterrors.ErrFieldSelectorUnknownField,
			fmt.Errorf("failed to resolve field %q: %w", field, err))
	}

	if resolvedField != nil {
		return []*SelectorField{resolvedField}, nil
	}

	if sr.fieldResolver != nil {
		refs := sr.fieldResolver.ResolveFieldName(field)
		if len(refs) > 0 {
			fields := make([]*SelectorField, 0, len(refs))
			for _, ref := range refs {
				resolvedField, err := resolve(ref)
				if err != nil {
					return nil, NewSelectorError(flterrors.ErrFieldSelectorUnknownField,
						fmt.Errorf("failed to resolve field %q: %w", field, err))
				}

				if resolvedField == nil {
					return nil, NewSelectorError(flterrors.ErrFieldSelectorUnknownField,
						fmt.Errorf("unable to resolve field name %q", ref))
				}
				fields = append(fields, resolvedField)
			}
			return fields, nil
		}
	}

	return nil, NewSelectorError(flterrors.ErrFieldSelectorUnknownField,
		fmt.Errorf("unable to resolve field name %q", field))
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

// SelectorError represents an error related to a selector, wrapping another error.
type SelectorError struct {
	SelectorError error
	OriginalError error
}

// NewSelectorError creates a new SelectorError.
func NewSelectorError(selectorError, originalError error) *SelectorError {
	return &SelectorError{
		SelectorError: selectorError,
		OriginalError: originalError,
	}
}

// Error returns the string representation of the SelectorError.
func (e *SelectorError) Error() string {
	return fmt.Sprintf("%s: %v", e.SelectorError.Error(), e.OriginalError)
}

// Unwrap returns the original error.
func (e *SelectorError) Unwrap() error {
	return e.OriginalError
}

// AsSelectorError checks if an error is of type SelectorError and assigns it to target.
func AsSelectorError(err error, target any) bool {
	if target == nil {
		return false
	}
	// Ensure target is a pointer to the correct type
	switch t := target.(type) {
	case **SelectorError:
		return errors.As(err, t)
	default:
		return false
	}
}

// IsSelectorError checks if an error is of type SelectorError.
func IsSelectorError(err error) bool {
	var selectorErr *SelectorError
	return errors.As(err, &selectorErr)
}

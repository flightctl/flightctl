package selector

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	gormschema "gorm.io/gorm/schema"
)

var (
	cacheStore = &sync.Map{}
)

// selectorFieldResolver is a struct that provides the ability to resolve selectors to
// their corresponding schema fields.
type selectorFieldResolver struct {
	schemaFields        map[SelectorName]*gormschema.Field
	selectorNameMapping SelectorNameMapping
	selectorResolver    SelectorResolver
}

// SelectorFieldResolver initializes a new selectorFieldResolver. It resolves schema fields from the provided model.
// If the model implements SelectorNameMapping or SelectorResolver, it will be used to resolve custom selectors.
func SelectorFieldResolver(model any) (Resolver, error) {
	resolved, err := ResolveFieldsFromSchema(model)
	if err != nil {
		return nil, err
	}

	fr := &selectorFieldResolver{schemaFields: resolved}
	if selectorNameMapping, ok := model.(SelectorNameMapping); ok {
		fr.selectorNameMapping = selectorNameMapping
	}
	if selectorResolver, ok := model.(SelectorResolver); ok {
		fr.selectorResolver = selectorResolver
	}
	return fr, nil
}

// ResolveNames maps a selector to its corresponding database field names.
// See ResolveFields for more details on the resolution process.
func (sr *selectorFieldResolver) ResolveNames(name SelectorName) ([]string, error) {
	resolvedFields, err := sr.ResolveFields(name)
	if err != nil {
		return nil, err
	}

	fields := make([]string, 0, len(resolvedFields))
	for _, selectorField := range resolvedFields {
		fields = append(fields, selectorField.FieldName)
	}
	return fields, nil
}

// ResolveFields resolves a selector name to its corresponding schema field(s).
// It supports resolving JSONB fields and custom field resolutions if selectorNameMapping or selectorResolver are present.
// It returns a slice of resolved SelectorField or an error if the selector cannot be resolved.
func (sr *selectorFieldResolver) ResolveFields(name SelectorName) ([]*SelectorField, error) {
	selectorNames := []SelectorName{name}
	if sr.selectorNameMapping != nil && sr.selectorNameMapping.ListSelectors().Contains(name) {
		if refs := sr.selectorNameMapping.MapSelectorName(name); len(refs) > 0 {
			selectorNames = refs
		}
	}

	fields := make([]*SelectorField, 0, len(selectorNames))
	for _, selectorName := range selectorNames {
		var resolvedField *SelectorField
		var err error

		// Attempt to resolve using selectorResolver if available, otherwise fallback to resolve function
		if sr.selectorResolver != nil && sr.selectorResolver.ListSelectors().Contains(selectorName) {
			resolvedField, err = sr.selectorResolver.ResolveSelector(selectorName)
		} else {
			resolvedField, err = sr.resolveSelector(selectorName)
		}

		if err != nil {
			return nil, err
		}
		if resolvedField == nil {
			continue
		}

		// Should not changed, explicitly assign it
		resolvedField.Name = selectorName

		// Handle JSONB cast types
		if resolvedField.IsJSONBCast() {
			switch resolvedField.Type {
			case Bool, Int, SmallInt, BigInt, Float, Timestamp, String:
				fields = append(fields, resolvedField)
			default:
				return nil, fmt.Errorf("casting to %q is not supported for JSONB fields", resolvedField.Type.String())
			}
		} else {
			fields = append(fields, resolvedField)
		}
	}

	if len(fields) > 0 && len(fields) != len(selectorNames) {
		fields = make([]*SelectorField, 0)
	}

	return fields, nil
}

func (sr *selectorFieldResolver) resolveSelector(name SelectorName) (*SelectorField, error) {
	if resolvedField, exists := sr.schemaFields[name]; exists {
		selectorType, ok := schemaTypeResolution[resolvedField.DataType]
		if !ok {
			return nil, fmt.Errorf("unknown or unsupported schema type for field: %s", resolvedField.DBName)
		}

		if selectorType.IsArray() {
			fieldKind := resolvedField.StructField.Type.Kind()
			if fieldKind != reflect.Array && fieldKind != reflect.Slice {
				return nil, fmt.Errorf("field %s is expected to be an array or slice, but got %s", resolvedField.DBName, fieldKind.String())
			}
		}

		// Parse the selector tag from the field's struct tag.
		// Safe to call because we only process fields with the `selector` tag.
		_, opt := parseSelectorTag(resolvedField.StructField.Tag.Get("selector"))

		return &SelectorField{
			Type:      selectorType,
			FieldName: resolvedField.DBName,
			FieldType: resolvedField.DataType,
			Options:   opt,
		}, nil
	}

	// Handle nested selector resolutions
	selectorName := name.String()
	for sn, schemaField := range sr.schemaFields {
		if len(selectorName) > len(sn.String()) && strings.HasPrefix(selectorName, sn.String()) {
			selectorType, ok := schemaTypeResolution[schemaField.DataType]
			if !ok {
				return nil, fmt.Errorf("unknown or unsupported schema type for field: %s", schemaField.DBName)
			}

			if selectorType.IsArray() && selectorName[len(sn.String())] == '[' {
				if !arrayPattern.MatchString(selectorName) {
					return nil, fmt.Errorf(
						"array access must specify a valid index (e.g., 'conditions[0]'); invalid selector: %s", selectorName)
				}

				fieldKind := schemaField.StructField.Type.Kind()
				if fieldKind != reflect.Array && fieldKind != reflect.Slice {
					return nil, fmt.Errorf("field %s is expected to be an array or slice, but got %s", schemaField.DBName, fieldKind.String())
				}

				arrayIndex, err := strconv.Atoi(selectorName[strings.Index(selectorName, "[")+1 : len(selectorName)-1])
				if err != nil {
					return nil, err
				}

				if arrayIndex == math.MaxInt {
					return nil, fmt.Errorf("array index overflow for selector %s", selectorName)
				}

				// 1-based indexing for PostgreSQL
				arrayIndex += 1

				// Parse the selector tag from the field's struct tag.
				// Safe to call because we only process fields with the `selector` tag.
				_, opt := parseSelectorTag(schemaField.StructField.Tag.Get("selector"))

				return &SelectorField{
					Type:      selectorType.ArrayType(),
					FieldName: fmt.Sprintf("%s[%d]", schemaField.DBName, arrayIndex),
					FieldType: schemaField.DataType,
					Options:   opt,
				}, nil

			}

			if selectorType == Jsonb && selectorName[len(sn.String())] == '.' {
				keyPath := schemaField.DBName + selectorName[len(sn.String()):]
				if strings.Contains(keyPath, "::") {
					return nil, fmt.Errorf("casting is not permitted: %s", selectorName)
				}

				// Parse the selector tag from the field's struct tag.
				// Safe to call because we only process fields with the `selector` tag.
				_, opt := parseSelectorTag(schemaField.StructField.Tag.Get("selector"))

				return &SelectorField{
					Type:      Jsonb,
					FieldName: keyPath,
					FieldType: schemaField.DataType,
					Options:   opt,
				}, nil
			}
		}
	}
	return nil, nil
}

// List returns a list of all selectors managed by the selectorFieldResolver.
func (sr *selectorFieldResolver) List() []SelectorName {
	set := NewSelectorFieldNameSet()
	for selector := range sr.schemaFields {
		set.Add(selector)
	}

	if sr.selectorNameMapping != nil {
		set.Add(sr.selectorNameMapping.ListSelectors().List()...)
	}

	if sr.selectorResolver != nil {
		set.Add(sr.selectorResolver.ListSelectors().List()...)
	}

	if set.Size() == 0 {
		return nil
	}

	supportedFields := slices.DeleteFunc(set.List(), func(sn SelectorName) bool {
		_, isHidden := sn.(hiddenSelectorName)
		return isHidden
	})

	sort.Slice(supportedFields, func(i, j int) bool {
		return supportedFields[i].String() < supportedFields[j].String()
	})

	return supportedFields
}

// ResolveFieldsFromSchema parses the schema of the given model and extracts the fields annotated with
// the `selector` tag. This is useful for determining which fields can be used in selector queries.
func ResolveFieldsFromSchema(dest any) (map[SelectorName]*gormschema.Field, error) {
	schema, err := gormschema.ParseWithSpecialTableName(dest, cacheStore, gormschema.NamingStrategy{IdentifierMaxLength: 64}, "")
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}

	selectorLst := make([]string, 0)
	fieldMap := make(map[SelectorName]*gormschema.Field)
	for _, field := range schema.Fields {
		if selector := strings.TrimSpace(field.StructField.Tag.Get("selector")); selector != "" && selector != "-" {
			name, opt := parseSelectorTag(selector)
			selectorLst = append(selectorLst, name)

			if _, ok := opt["hidden"]; ok {
				// Mark the field as a hidden selector.
				fieldMap[NewHiddenSelectorName(name)] = field
			} else {
				// Mark the field as a regular selector.
				fieldMap[NewSelectorName(name)] = field
			}
		}
	}

	if err := isPrefixOfAnother(selectorLst); err != nil {
		return nil, fmt.Errorf("found conflicted selectors: %w", err)
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

// isPrefixOfAnother checks if any selector is a prefix of another in the list.
func isPrefixOfAnother(selectors []string) error {
	sort.Strings(selectors)
	for i := 0; i < len(selectors)-1; i++ {
		if strings.HasPrefix(selectors[i+1], selectors[i]+".") {
			return fmt.Errorf("'%s' is a prefix of '%s'", selectors[i], selectors[i+1])
		}
	}
	return nil
}

// parseSelectorTag parses the `selector` tag from a GORM schema field.
// The tag is expected to contain a field name followed by optional comma-separated flags or options,
// e.g., "metadata.label,hidden,private".
// Returns the field name and a set of parsed options or flags as a map.
func parseSelectorTag(tag string) (string, map[string]struct{}) {
	if strings.TrimSpace(tag) == "" {
		return "", nil
	}

	// Split the tag into parts
	parts := strings.Split(tag, ",")
	fieldName := strings.TrimSpace(parts[0])

	// Initialize the map for options
	options := make(map[string]struct{})

	// Process additional options or flags
	for _, opt := range parts[1:] {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		options[opt] = struct{}{}
	}

	return fieldName, options
}

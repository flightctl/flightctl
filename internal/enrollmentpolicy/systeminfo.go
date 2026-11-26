package enrollmentpolicy

import (
	"iter"
	"reflect"
	"strings"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"github.com/flightctl/flightctl/internal/domain"
)

const systemInfoRoot = "systemInfo"

var systemInfoType = reflect.TypeFor[domain.DeviceSystemInfo]()

type reportField struct {
	index     int
	valueType reflect.Type
	omitEmpty bool
}

func reportFields(object reflect.Type) iter.Seq2[string, reportField] {
	return func(yield func(string, reportField) bool) {
		for index := range object.NumField() {
			field := object.Field(index)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			descriptor := reportField{
				index:     index,
				valueType: field.Type,
				omitEmpty: strings.Contains(","+options+",", ",omitempty,"),
			}
			if !yield(name, descriptor) {
				return
			}
		}
	}
}

// reportObject binds the requested fields using the API's JSON field names.
// Nested values are adapted lazily as CEL traverses them.
func reportObject(value reflect.Value, fields map[string]struct{}) map[string]any {
	object := make(map[string]any)
	for name, descriptor := range reportFields(value.Type()) {
		if _, selected := fields[name]; fields != nil && !selected {
			continue
		}
		field := value.Field(descriptor.index)
		if !descriptor.omitEmpty || !emptyJSONValue(field) {
			object[name] = field.Interface()
		}
	}
	return object
}

func emptyJSONValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Struct:
		return false
	default:
		return value.IsZero()
	}
}

func indirectType(kind reflect.Type) reflect.Type {
	for kind != nil && kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	return kind
}

func reportFieldType(object reflect.Type, name string) reflect.Type {
	object = indirectType(object)
	if object == nil {
		return nil
	}
	switch object.Kind() {
	case reflect.Map:
		if object.Key().Kind() == reflect.String {
			return object.Elem()
		}
	case reflect.Struct:
		for key, field := range reportFields(object) {
			if key == name {
				return field.valueType
			}
		}
		if object == systemInfoType {
			// The API flattens additional string properties into systemInfo.
			return reflect.TypeFor[string]()
		}
	}
	return nil
}

// reportAdapter exposes native API values as CEL values without copying
// arbitrary maps, lists, or strings before the expression reads them.
type reportAdapter struct {
	oversized bool
}

func (adapter *reportAdapter) NativeToValue(value any) ref.Val {
	if resolved, ok := value.(ref.Val); ok {
		return resolved
	}
	field := reflect.ValueOf(value)
	for field.Kind() == reflect.Pointer {
		field = field.Elem()
	}
	switch field.Kind() {
	case reflect.Invalid:
		return types.NullValue
	case reflect.Struct:
		return types.NewStringInterfaceMap(adapter, reportObject(field, nil))
	case reflect.Map:
		if field.IsNil() {
			return types.NullValue
		}
		return types.NewDynamicMap(adapter, field.Interface())
	case reflect.Array, reflect.Slice:
		if field.Kind() == reflect.Slice && field.IsNil() {
			return types.NullValue
		}
		return types.NewDynamicList(adapter, field.Interface())
	case reflect.String:
		if field.Len() > maxReportedStringBytes {
			// Collection equality can consume an element error. Record the
			// limit failure for the entire evaluation.
			adapter.oversized = true
			return types.WrapErr(ErrReportedStringTooLong)
		}
		return types.String(field.String())
	default:
		return types.DefaultTypeAdapter.NativeToValue(field.Interface())
	}
}

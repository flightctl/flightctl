package model

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/selector"
)

type selectorTest struct {
	APISchemaName string
	APISchema     any
	Selectors     selectorToTypeMap
}

func TestModelSchemaSelectors(t *testing.T) {
	testSelectors := []selectorTest{
		{"status", &api.DeviceStatus{}, deviceStatusSelectors},
		{"status", &api.EnrollmentRequestStatus{}, enrollmentRequestStatusSelectors},
		{"status", &api.CertificateSigningRequestStatus{}, certificateSigningRequestStatusSelectors},
		{"spec", &api.FleetSpec{}, fleetSpecSelectors},
		{"spec", &api.ResourceSyncSpec{}, resourceSyncSpecSelectors},
		{"spec", &api.GenericRepoSpec{}, repositorySpecSelectors},
	}

	for _, test := range testSelectors {
		if err := verifySchema(test.APISchemaName, test.APISchema, test.Selectors); err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
		}
	}
}

func verifySchema(schemaName string, apischema any, selectors selectorToTypeMap) error {
	schema := scanAPISchema(schemaName, apischema)
	for selector, typ := range selectors {
		schemaTyp, exists := schema[selector]
		if !exists {
			return fmt.Errorf("%v: does not exist in API schema", selector)
		}

		if schemaTyp != typ {
			return fmt.Errorf("%v: defined types and schema types do not match", selector)
		}
	}
	return nil
}

// scanAPISchema scans an API type definition and returns a map of field paths to their types.
func scanAPISchema(schemaName string, apischema any) selectorToTypeMap {
	result := make(selectorToTypeMap)
	typ := reflect.TypeOf(apischema)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	rootName := selector.SelectorName(schemaName)
	dfsType(typ, rootName, result)
	return result
}

func dfsType(typ reflect.Type, path selector.SelectorName, result selectorToTypeMap) {
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldType := field.Type

		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		fieldPath := path + "." + selector.SelectorName(name)
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		// Handle special case for time.Time to assign it to Timestamp
		if fieldType == reflect.TypeOf(time.Time{}) {
			result[fieldPath] = selector.Timestamp
			continue
		}

		// Handle each kind explicitly with corresponding constant types
		switch fieldType.Kind() {
		case reflect.Bool:
			result[fieldPath] = selector.Bool
		case reflect.Int, reflect.Int32:
			result[fieldPath] = selector.Int
		case reflect.Int8, reflect.Int16:
			result[fieldPath] = selector.SmallInt
		case reflect.Int64:
			result[fieldPath] = selector.BigInt
		case reflect.Uint, reflect.Uint32:
			result[fieldPath] = selector.Int
		case reflect.Uint8, reflect.Uint16:
			result[fieldPath] = selector.SmallInt
		case reflect.Uint64, reflect.Uintptr:
			result[fieldPath] = selector.BigInt
		case reflect.Float32, reflect.Float64:
			result[fieldPath] = selector.Float
		case reflect.String:
			result[fieldPath] = selector.String
		case reflect.Slice, reflect.Array:
			// Assign slice and array types and recurse if the element is a struct
			elemType := fieldType.Elem()
			for elemType.Kind() == reflect.Ptr {
				elemType = elemType.Elem()
			}
			result[fieldPath] = getArrayTypeConstant(elemType)
			if elemType.Kind() == reflect.Struct {
				dfsType(elemType, fieldPath+"[]", result)
			}
		case reflect.Struct:
			// Recurse into nested struct fields
			dfsType(fieldType, fieldPath, result)
		default:
			// For any other unknown types, use Jsonb as the default
			result[fieldPath] = selector.Jsonb
		}
	}
}

// getArrayTypeConstant returns the corresponding array constant based on the element type
func getArrayTypeConstant(t reflect.Type) selector.SelectorType {
	switch t.Kind() {
	case reflect.Bool:
		return selector.BoolArray
	case reflect.Int, reflect.Int32:
		return selector.IntArray
	case reflect.Int8, reflect.Int16:
		return selector.SmallIntArray
	case reflect.Int64:
		return selector.BigIntArray
	case reflect.Float32, reflect.Float64:
		return selector.FloatArray
	case reflect.String:
		return selector.TextArray
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return selector.TimestampArray
		}
		return selector.Jsonb
	case reflect.Uint8:
		// Treat []byte as a string
		return selector.String
	default:
		return selector.Unknown
	}
}

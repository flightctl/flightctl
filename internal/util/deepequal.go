package util

import (
	"encoding/json"
	"reflect"
	"sync"
)

// typeInfo caches reflection metadata for types to avoid repeated computation
type typeInfo struct {
	hasUnionFields bool  // true if type contains json.RawMessage fields (union types)
	unionFields    []int // indices of json.RawMessage fields
}

var (
	typeCache = make(map[reflect.Type]*typeInfo)
	cacheMu   sync.RWMutex
)

// DeepEqual provides optimized deep equality comparison with special handling
// for union types (json.RawMessage fields) that require JSON normalization.
func DeepEqual(x, y interface{}) bool {
	if x == nil || y == nil {
		return x == y
	}

	v1 := reflect.ValueOf(x)
	v2 := reflect.ValueOf(y)

	if v1.Type() != v2.Type() {
		return false
	}

	result := deepValueEqual(v1, v2, make(map[visit]bool))

	// Fallback: if deep equality fails but both values can be marshaled to JSON
	// and produce identical JSON, consider them equal. This handles cases where
	// internal struct state differs but the externally visible data is identical.
	if !result {
		json1, err1 := json.Marshal(x)
		json2, err2 := json.Marshal(y)
		if err1 == nil && err2 == nil && string(json1) == string(json2) {
			return true
		}
	}

	return result
}

type visit struct {
	a1  uintptr
	a2  uintptr
	typ reflect.Type
}

func deepValueEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	if !v1.IsValid() || !v2.IsValid() {
		return v1.IsValid() == v2.IsValid()
	}
	if v1.Type() != v2.Type() {
		return false
	}

	// Cycle detection
	if v1.CanAddr() && v2.CanAddr() {
		addr1 := v1.UnsafeAddr()
		addr2 := v2.UnsafeAddr()
		if addr1 > addr2 {
			addr1, addr2 = addr2, addr1
		}
		typ := v1.Type()
		v := visit{addr1, addr2, typ}
		if visited[v] {
			return true
		}
		visited[v] = true
	}

	switch v1.Kind() {
	case reflect.Array:
		return arrayEqual(v1, v2, visited)
	case reflect.Slice:
		return sliceEqual(v1, v2, visited)
	case reflect.Interface:
		return interfaceEqual(v1, v2, visited)
	case reflect.Pointer:
		return pointerEqual(v1, v2, visited)
	case reflect.Struct:
		return structEqual(v1, v2, visited)
	case reflect.Map:
		return mapEqual(v1, v2, visited)
	case reflect.Func:
		if v1.IsNil() && v2.IsNil() {
			return true
		}
		return false // Functions are only equal if both are nil
	default:
		// Basic types, strings, etc.
		return basicEqual(v1, v2)
	}
}

func arrayEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	for i := 0; i < v1.Len(); i++ {
		if !deepValueEqual(v1.Index(i), v2.Index(i), visited) {
			return false
		}
	}
	return true
}

func sliceEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	if v1.IsNil() != v2.IsNil() {
		return false
	}
	if v1.Len() != v2.Len() {
		return false
	}
	if v1.Pointer() == v2.Pointer() {
		return true
	}

	// Special handling for []byte - much faster than element by element
	if v1.Type().Elem().Kind() == reflect.Uint8 {
		return string(v1.Bytes()) == string(v2.Bytes())
	}

	for i := 0; i < v1.Len(); i++ {
		if !deepValueEqual(v1.Index(i), v2.Index(i), visited) {
			return false
		}
	}
	return true
}

func interfaceEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	if v1.IsNil() || v2.IsNil() {
		return v1.IsNil() == v2.IsNil()
	}
	return deepValueEqual(v1.Elem(), v2.Elem(), visited)
}

func pointerEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	if v1.Pointer() == v2.Pointer() {
		return true
	}
	return deepValueEqual(v1.Elem(), v2.Elem(), visited)
}

func structEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	typ := v1.Type()

	// Get or compute type info
	info := getTypeInfo(typ)

	// If struct has union fields (json.RawMessage), use JSON normalization for those fields
	if info.hasUnionFields {
		return structEqualWithUnions(v1, v2, visited, info)
	}

	// Fast path for structs without union fields
	for i := 0; i < v1.NumField(); i++ {
		if !deepValueEqual(v1.Field(i), v2.Field(i), visited) {
			return false
		}
	}
	return true
}

func structEqualWithUnions(v1, v2 reflect.Value, visited map[visit]bool, info *typeInfo) bool {
	for i := 0; i < v1.NumField(); i++ {
		field1 := v1.Field(i)
		field2 := v2.Field(i)

		// Check if this is a union field (json.RawMessage)
		isUnionField := false
		for _, unionIdx := range info.unionFields {
			if i == unionIdx {
				isUnionField = true
				break
			}
		}

		if isUnionField {
			// Use JSON normalization for union fields
			if !jsonRawMessageEqual(field1, field2) {
				return false
			}
		} else {
			// Use regular deep comparison for non-union fields
			if !deepValueEqual(field1, field2, visited) {
				return false
			}
		}
	}
	return true
}

func mapEqual(v1, v2 reflect.Value, visited map[visit]bool) bool {
	if v1.IsNil() != v2.IsNil() {
		return false
	}
	if v1.Len() != v2.Len() {
		return false
	}
	if v1.Pointer() == v2.Pointer() {
		return true
	}

	for _, k := range v1.MapKeys() {
		val1 := v1.MapIndex(k)
		val2 := v2.MapIndex(k)
		if !val2.IsValid() || !deepValueEqual(val1, val2, visited) {
			return false
		}
	}
	return true
}

func basicEqual(v1, v2 reflect.Value) bool {
	switch v1.Kind() {
	case reflect.Bool:
		return v1.Bool() == v2.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v1.Int() == v2.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v1.Uint() == v2.Uint()
	case reflect.Float32, reflect.Float64:
		return v1.Float() == v2.Float()
	case reflect.Complex64, reflect.Complex128:
		return v1.Complex() == v2.Complex()
	case reflect.String:
		return v1.String() == v2.String()
	default:
		return false
	}
}

func jsonRawMessageEqual(v1, v2 reflect.Value) bool {
	// Handle json.RawMessage fields with JSON normalization
	// Check if the types are json.RawMessage
	if v1.Type() != reflect.TypeOf(json.RawMessage{}) || v2.Type() != reflect.TypeOf(json.RawMessage{}) {
		// Fall back to regular comparison if not json.RawMessage
		return deepValueEqual(v1, v2, make(map[visit]bool))
	}

	// Handle the case where we can't get Interface() (unexported fields)
	var raw1, raw2 json.RawMessage

	if v1.CanInterface() {
		raw1 = v1.Interface().(json.RawMessage)
	} else {
		// For unexported fields, manually copy the bytes
		if v1.Kind() == reflect.Slice && v1.Type().Elem().Kind() == reflect.Uint8 {
			raw1 = make([]byte, v1.Len())
			for i := 0; i < v1.Len(); i++ {
				raw1[i] = byte(v1.Index(i).Uint())
			}
		} else {
			// Fall back to regular comparison for non-slice types
			return deepValueEqual(v1, v2, make(map[visit]bool))
		}
	}

	if v2.CanInterface() {
		raw2 = v2.Interface().(json.RawMessage)
	} else {
		// For unexported fields, manually copy the bytes
		if v2.Kind() == reflect.Slice && v2.Type().Elem().Kind() == reflect.Uint8 {
			raw2 = make([]byte, v2.Len())
			for i := 0; i < v2.Len(); i++ {
				raw2[i] = byte(v2.Index(i).Uint())
			}
		} else {
			// Fall back to regular comparison for non-slice types
			return deepValueEqual(v1, v2, make(map[visit]bool))
		}
	}

	// Handle nil/empty cases
	if len(raw1) == 0 && len(raw2) == 0 {
		return true
	}
	if len(raw1) == 0 || len(raw2) == 0 {
		return false
	}

	// Quick check: if the raw bytes are identical, they're equal
	if string(raw1) == string(raw2) {
		return true
	}

	// Normalize JSON and compare
	var obj1, obj2 interface{}
	if err := json.Unmarshal(raw1, &obj1); err != nil {
		return false
	}
	if err := json.Unmarshal(raw2, &obj2); err != nil {
		return false
	}

	return reflect.DeepEqual(obj1, obj2)
}

func getTypeInfo(typ reflect.Type) *typeInfo {
	cacheMu.RLock()
	if info, exists := typeCache[typ]; exists {
		cacheMu.RUnlock()
		return info
	}
	cacheMu.RUnlock()

	// Compute type info
	info := &typeInfo{}
	if typ.Kind() == reflect.Struct {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type == reflect.TypeOf(json.RawMessage{}) {
				info.hasUnionFields = true
				info.unionFields = append(info.unionFields, i)
			}
		}
	}

	// Cache it
	cacheMu.Lock()
	typeCache[typ] = info
	cacheMu.Unlock()

	return info
}

package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/template"
)

type DeviceCompletionCount struct {
	Count               int64
	SameRenderedVersion bool
	SameTemplateVersion bool
	UpdatingReason      UpdateState
	UpdateTimedOut      bool
}

type HookActionType string

const (
	HookActionTypeRun HookActionType = "run"
)

type HookConditionType string

const (
	HookConditionTypePathOp     HookConditionType = "path"
	HookConditionTypeExpression HookConditionType = "expression"
)

type ConfigProviderType string

const (
	GitConfigProviderType        ConfigProviderType = "gitRef"
	HttpConfigProviderType       ConfigProviderType = "httpRef"
	InlineConfigProviderType     ConfigProviderType = "inline"
	KubernetesSecretProviderType ConfigProviderType = "secretRef"
)

type ApplicationProviderType string

const (
	ImageApplicationProviderType  ApplicationProviderType = "image"
	InlineApplicationProviderType ApplicationProviderType = "inline"
)

type ApplicationVolumeProviderType string

const (
	ImageApplicationVolumeProviderType ApplicationVolumeProviderType = "image"
)

// Type returns the type of the action.
func (t HookAction) Type() (HookActionType, error) {
	var data map[HookActionType]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		return "", err
	}

	types := []HookActionType{
		HookActionTypeRun,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook action type: %+v", data)
}

// Type returns the type of the condition.
func (t HookCondition) Type() (HookConditionType, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		var data HookConditionExpression
		if err := json.Unmarshal(t.union, &data); err != nil {
			return "", err
		}
		return HookConditionTypeExpression, nil
	}

	types := []HookConditionType{
		HookConditionTypePathOp,
	}
	for _, t := range types {
		if _, exists := data[string(t)]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook condition type: %+v", data)
}

// Type returns the type of the config provider.
func (c ConfigProviderSpec) Type() (ConfigProviderType, error) {
	var data map[ConfigProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	types := []ConfigProviderType{
		GitConfigProviderType,
		HttpConfigProviderType,
		InlineConfigProviderType,
		KubernetesSecretProviderType,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine config provider type: %+v", data)
}

// Type returns the type of the application provider.
func (a ApplicationProviderSpec) Type() (ApplicationProviderType, error) {
	return getApplicationType(a.union)
}

func getApplicationType(union json.RawMessage) (ApplicationProviderType, error) {
	var data map[ApplicationProviderType]interface{}
	if err := json.Unmarshal(union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationProviderType]; exists {
		return ImageApplicationProviderType, nil
	}

	if _, exists := data[InlineApplicationProviderType]; exists {
		return InlineApplicationProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application provider type: %+v", data)
}

func (c ApplicationVolume) Type() (ApplicationVolumeProviderType, error) {
	var data map[ApplicationVolumeProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationVolumeProviderType]; exists {
		return ImageApplicationVolumeProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application volume type: %+v", data)
}

func PercentageAsInt(p Percentage) (int, error) {
	index := strings.Index(p, "%")
	if index <= 0 || index != len(p)-1 {
		return 0, fmt.Errorf("%s is not in percentage format", p)
	}
	percentage, err := strconv.ParseInt(p[:index], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse percentage value: %w", err)
	}
	if percentage < 0 || percentage > 100 {
		return 0, fmt.Errorf("percentage must be between 0 and 100, got %d", percentage)
	}
	return int(percentage), nil
}

func DeviceSpecsAreEqual(d1, d2 DeviceSpec) bool {
	return deepEqualWithSpecialHandling(reflect.ValueOf(d1), reflect.ValueOf(d2))
}

// deepEqualWithSpecialHandling performs deep comparison with special handling for union types
func deepEqualWithSpecialHandling(v1, v2 reflect.Value) bool {
	if v1.Type() != v2.Type() {
		return false
	}

	switch v1.Kind() {
	case reflect.Ptr:
		if v1.IsNil() && v2.IsNil() {
			return true
		}
		if v1.IsNil() || v2.IsNil() {
			return false
		}
		return deepEqualWithSpecialHandling(v1.Elem(), v2.Elem())

	case reflect.Slice:
		if v1.IsNil() && v2.IsNil() {
			return true
		}
		if v1.IsNil() || v2.IsNil() {
			return false
		}
		if v1.Len() != v2.Len() {
			return false
		}
		for i := 0; i < v1.Len(); i++ {
			if !deepEqualWithSpecialHandling(v1.Index(i), v2.Index(i)) {
				return false
			}
		}
		return true

	case reflect.Struct:
		// Special handling for union types that contain json.RawMessage
		if isUnionType(v1.Type()) {
			return compareUnionTypeAsJSON(v1, v2)
		}

		// Regular struct comparison
		for i := 0; i < v1.NumField(); i++ {
			if !deepEqualWithSpecialHandling(v1.Field(i), v2.Field(i)) {
				return false
			}
		}
		return true

	case reflect.Map:
		if v1.IsNil() && v2.IsNil() {
			return true
		}
		if v1.IsNil() || v2.IsNil() {
			return false
		}
		if v1.Len() != v2.Len() {
			return false
		}
		for _, key := range v1.MapKeys() {
			val1 := v1.MapIndex(key)
			val2 := v2.MapIndex(key)
			if !val2.IsValid() || !deepEqualWithSpecialHandling(val1, val2) {
				return false
			}
		}
		return true

	default:
		// For basic types, use reflect.DeepEqual
		return reflect.DeepEqual(v1.Interface(), v2.Interface())
	}
}

// isUnionType checks if a type is one of our known union types that contain json.RawMessage
func isUnionType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}

	// Check if struct contains a json.RawMessage field named "union"
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Name == "union" && field.Type == reflect.TypeOf(json.RawMessage{}) {
			return true
		}
	}
	return false
}

// compareUnionTypeAsJSON compares union types using normalized JSON serialization
func compareUnionTypeAsJSON(v1, v2 reflect.Value) bool {
	json1, err1 := json.Marshal(v1.Interface())
	json2, err2 := json.Marshal(v2.Interface())

	if err1 != nil || err2 != nil {
		return false
	}

	// Normalize JSON by unmarshaling and remarshaling to ensure consistent formatting
	var obj1, obj2 interface{}
	if err := json.Unmarshal(json1, &obj1); err != nil {
		return false
	}
	if err := json.Unmarshal(json2, &obj2); err != nil {
		return false
	}

	// Use reflect.DeepEqual on the unmarshaled objects for semantic comparison
	return reflect.DeepEqual(obj1, obj2)
}

func FleetSpecsAreEqual(f1, f2 FleetSpec) bool {
	// Use JSON comparison for consistent, automatic handling of all fields
	json1, err := json.Marshal(f1)
	if err != nil {
		return false
	}
	json2, err := json.Marshal(f2)
	if err != nil {
		return false
	}
	return bytes.Equal(json1, json2)
}

// Some functions that we provide to users.  In case of a missing label,
// we may get an interface{} rather than string because
// ExecuteGoTemplateOnDevice() converts the Device struct to a map.
// Therefore our functions here need to ensure we get a string, and if
// not then they return an empty string.  Note that this will only
// happen if the "missingkey=zero" option is used in the template.  If
// "missingkey=error" is used, the template execution will fail and we
// won't get to this point.
func GetGoTemplateFuncMap() template.FuncMap {
	stringOrDefault := func(s any) string {
		str, ok := s.(string)
		if ok {
			return str
		}
		strPtr, ok := s.(*string)
		if ok && strPtr != nil {
			return *strPtr
		}
		return ""
	}

	toUpper := func(s any) string {
		return strings.ToUpper(stringOrDefault(s))
	}

	toLower := func(s any) string {
		return strings.ToLower(stringOrDefault(s))
	}

	replace := func(old, new string, input any) string {
		return strings.Replace(stringOrDefault(input), old, new, -1)
	}

	getOrDefault := func(m *map[string]string, key string, defaultValue string) string {
		if m == nil {
			return defaultValue
		}
		if val, ok := (*m)[key]; ok {
			return val
		}
		return defaultValue
	}

	return template.FuncMap{
		"upper":        toUpper,
		"lower":        toLower,
		"replace":      replace,
		"getOrDefault": getOrDefault,
	}
}

// This function wraps template.Execute.  Instead of passing the device directly,
// it converts it into a map first.  This has two purposes:
// 1. The user-provided template uses the yaml/json API format (e.g., lower case)
// 2. The map contains only the device fields we allow access to
func ExecuteGoTemplateOnDevice(t *template.Template, dev *Device) (string, error) {
	devMap := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   dev.Metadata.Name,
			"labels": dev.Metadata.Labels,
		},
	}

	buf := new(bytes.Buffer)
	err := t.Execute(buf, devMap)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// MatchExpressionsToString converts a list of MatchExpressions into a formatted string.
// Each MatchExpression is represented by its string form, separated by ", ".
func MatchExpressionsToString(exprs ...MatchExpression) string {
	if len(exprs) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, e := range exprs {
		sb.WriteString(e.String())
		if i < len(exprs)-1 {
			sb.WriteString(", ")
		}
	}
	return sb.String()
}

// String converts a MatchExpression into its string representation.
// Example formats:
// - Exists: "key"
// - DoesNotExist: "!key"
// - In: "key in (val1, val2)"
// - NotIn: "key notin (val1, val2)"
func (e MatchExpression) String() string {
	var sb strings.Builder

	switch e.Operator {
	case Exists:
		sb.WriteString(e.Key) // Exists: Just the key
	case DoesNotExist:
		sb.WriteString("!") // Prepend the "not exists" operator
		sb.WriteString(e.Key)
	case In:
		if e.Values != nil {
			sb.WriteString(e.Key)
			sb.WriteString(" in ")
			sb.WriteString("(" + strings.Join(*e.Values, ", ") + ")")
		}
	case NotIn:
		if e.Values != nil {
			sb.WriteString(e.Key)
			sb.WriteString(" notin ")
			sb.WriteString("(" + strings.Join(*e.Values, ", ") + ")")
		}
	default:
		// Return empty string for unsupported operators
		return ""
	}
	return sb.String()
}

// GetConsoles returns the list of DeviceConsole objects, or an empty list if the field is nil.
func (rd DeviceSpec) GetConsoles() []DeviceConsole {
	if rd.Consoles == nil {
		return []DeviceConsole{}
	} else {
		return *rd.Consoles
	}
}

func GetNextDeviceRenderedVersion(annotations map[string]string) (string, error) {
	var currentRenderedVersion int64 = 0
	var err error
	renderedVersionString, ok := annotations[DeviceAnnotationRenderedVersion]
	if ok {
		currentRenderedVersion, err = strconv.ParseInt(renderedVersionString, 10, 64)
		if err != nil {
			return "", err
		}
	}

	currentRenderedVersion++
	return strconv.FormatInt(currentRenderedVersion, 10), nil
}

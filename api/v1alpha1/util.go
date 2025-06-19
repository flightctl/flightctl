package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/samber/lo"
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

func configsAreEqual(c1, c2 *[]ConfigProviderSpec) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ConfigProviderSpec, item2 ConfigProviderSpec) bool {
		type1, err := item1.Type()
		if err != nil {
			return false
		}
		type2, err := item2.Type()
		if err != nil {
			return false
		}
		if type1 != type2 {
			return false
		}

		switch type1 {
		case GitConfigProviderType:
			c1, err := item1.AsGitConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsGitConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case HttpConfigProviderType:
			c1, err := item1.AsHttpConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsHttpConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case InlineConfigProviderType:
			c1, err := item1.AsInlineConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsInlineConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case KubernetesSecretProviderType:
			c1, err := item1.AsKubernetesSecretProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsKubernetesSecretProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		default:
			return false
		}
	})
}

func applicationsAreEqual(c1, c2 *[]ApplicationProviderSpec) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ApplicationProviderSpec, item2 ApplicationProviderSpec) bool {
		type1, err := item1.Type()
		if err != nil {
			return false
		}
		type2, err := item2.Type()
		if err != nil {
			return false
		}
		if type1 != type2 {
			return false
		}

		switch type1 {
		case ImageApplicationProviderType:
			imageSpec1, err := item1.AsImageApplicationProviderSpec()
			if err != nil {
				return false
			}
			imageSpec2, err := item2.AsImageApplicationProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(imageSpec1, imageSpec2)
		case InlineApplicationProviderType:
			inlineSpec1, err := item1.AsInlineApplicationProviderSpec()
			if err != nil {
				return false
			}
			inlineSpec2, err := item2.AsInlineApplicationProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(inlineSpec1, inlineSpec2)
		default:
			return false
		}
	})
}

func resourcesAreEqual(c1, c2 *[]ResourceMonitor) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ResourceMonitor, item2 ResourceMonitor) bool {
		value1, err := item1.ValueByDiscriminator()
		if err != nil {
			return false
		}
		value2, err := item2.ValueByDiscriminator()
		if err != nil {
			return false
		}
		return reflect.DeepEqual(value1, value2)
	})
}

func DeviceSpecsAreEqual(d1, d2 DeviceSpec) bool {
	// Check OS
	if !reflect.DeepEqual(d1.Os, d2.Os) {
		return false
	}

	// Check Config
	if !configsAreEqual(d1.Config, d2.Config) {
		return false
	}

	// Check Applications
	if !applicationsAreEqual(d1.Applications, d2.Applications) {
		return false
	}

	// Check Systemd
	if !reflect.DeepEqual(d1.Systemd, d2.Systemd) {
		return false
	}

	// Check Resources
	if !resourcesAreEqual(d1.Resources, d2.Resources) {
		return false
	}

	// Check Decommission
	if !reflect.DeepEqual(d1.Decommissioning, d2.Decommissioning) {
		return false
	}

	// Check UpdatePolicy
	if !reflect.DeepEqual(d1.UpdatePolicy, d2.UpdatePolicy) {
		return false
	}

	return true
}

func FleetSpecsAreEqual(f1, f2 FleetSpec) bool {
	if !reflect.DeepEqual(f1.Selector, f2.Selector) {
		return false
	}

	if !reflect.DeepEqual(f1.Template.Metadata, f2.Template.Metadata) {
		return false
	}

	return DeviceSpecsAreEqual(f1.Template.Spec, f2.Template.Spec)
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

package templating

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/flightctl/flightctl/internal/domain"
)

// GetGoTemplateFuncMap returns the function map for device templating.
// Some functions that we provide to users. In case of a missing label,
// we may get an interface{} rather than string because
// ExecuteGoTemplateOnDevice() converts the Device struct to a map.
// Therefore our functions here need to ensure we get a string, and if
// not then they return an empty string. Note that this will only
// happen if the "missingkey=zero" option is used in the template. If
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

// ExecuteGoTemplateOnDevice wraps template.Execute. Instead of passing the device directly,
// it converts it into a map first. This has two purposes:
// 1. The user-provided template uses the yaml/json API format (e.g., lower case)
// 2. The map contains only the device fields we allow access to
func ExecuteGoTemplateOnDevice(t *template.Template, dev *domain.Device) (string, error) {
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

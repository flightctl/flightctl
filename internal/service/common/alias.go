package common

import (
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
)

const (
	// maxLabelValueLength is the maximum length for a Kubernetes label value.
	maxLabelValueLength = 63
)

// labelValueRegex matches valid Kubernetes label value: [a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?
var labelValueRegex = regexp.MustCompile(`^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$`)

const customInfoPrefix = "customInfo."

// ComputeDefaultAlias returns the first non-empty value from systemInfo for the given keys, sanitized for use as a label value.
// Keys can be: fixed DeviceSystemInfo fields (architecture, bootID, operatingSystem, agentVersion),
// customInfo.<key> for CustomInfo[key], or any key for AdditionalProperties (e.g. hostname, productSerial).
// Returns empty string if no non-empty value is found or the result would be invalid as a label value.
func ComputeDefaultAlias(systemInfo domain.DeviceSystemInfo, keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		val := getSystemInfoValue(systemInfo, key)
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		if out := sanitizeLabelValue(val); out != "" {
			return out
		}
	}
	return ""
}

// getSystemInfoValue returns the value for key from DeviceSystemInfo: fixed fields, customInfo.<key>, or additionalProperties.
func getSystemInfoValue(systemInfo domain.DeviceSystemInfo, key string) string {
	switch key {
	case "architecture":
		return systemInfo.Architecture
	case "bootID":
		return systemInfo.BootID
	case "operatingSystem":
		return systemInfo.OperatingSystem
	case "agentVersion":
		return systemInfo.AgentVersion
	}
	if strings.HasPrefix(key, customInfoPrefix) {
		suffix := key[len(customInfoPrefix):]
		if systemInfo.CustomInfo != nil {
			if v, ok := (*systemInfo.CustomInfo)[suffix]; ok {
				return v
			}
		}
		return ""
	}
	val, _ := systemInfo.Get(key)
	return val
}

// sanitizeLabelValue truncates and sanitizes s for use as a Kubernetes label value (max 63 chars, valid pattern).
// Returns empty string if the result would be invalid.
func sanitizeLabelValue(s string) string {
	// Strip invalid characters: keep alphanumeric, '-', '_', '.'
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	s = b.String()
	if len(s) > maxLabelValueLength {
		s = s[:maxLabelValueLength]
	}
	s = strings.Trim(s, "-_.")
	if s == "" || !labelValueRegex.MatchString(s) {
		return ""
	}
	return s
}

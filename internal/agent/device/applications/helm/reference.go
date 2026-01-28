package helm

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/client"
)

const helmReleaseNameMaxLength = 53

// SanitizeReleaseName combines a chart name and version into a valid Helm release name.
// It lowercases the input, replaces invalid characters with hyphens,
// removes consecutive hyphens, trims leading/trailing hyphens, and truncates to 53 characters.
func SanitizeReleaseName(chartName string) (string, error) {
	name, version, err := client.ParseChartRef(chartName)
	if err != nil {
		return "", fmt.Errorf("parsing chart reference: %w", err)
	}
	return sanitize(fmt.Sprintf("%s-%s", name, version)), nil
}

func sanitize(name string) string {
	if name == "" {
		return ""
	}

	name = strings.ToLower(name)

	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}

	sanitized := result.String()

	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}

	sanitized = strings.Trim(sanitized, "-")

	if len(sanitized) > helmReleaseNameMaxLength {
		sanitized = sanitized[:helmReleaseNameMaxLength]
		sanitized = strings.TrimRight(sanitized, "-")
	}

	return sanitized
}

package kubeflowmodelregistrysource

import (
	"fmt"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxDNSSubdomainLength = 253
	maxDNSLabelLength     = 63
)

// normalizeName maps a free-form RegisteredModel name to a valid Kubernetes
// DNS subdomain suitable for metadata.name.
//
// The original name remains unchanged in CatalogItem.spec.displayName.
//
// Normalization rules:
//
//  1. ASCII uppercase letters are converted to lowercase.
//  2. ASCII lowercase letters and digits are preserved.
//  3. Original dots remain DNS-label boundaries.
//  4. Every run of other characters, including non-ASCII characters, becomes
//     one hyphen within its label. Non-ASCII text is never transliterated.
//  5. Empty labels are removed and leading or trailing hyphens are stripped.
//  6. Each label is limited to 63 bytes.
//  7. The complete name is limited to 253 bytes.
//  8. An empty result is rejected.
func normalizeName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name is empty")
	}

	var normalized strings.Builder
	normalized.Grow(len(name))

	separatorPending := false

	for _, r := range name {
		switch {
		case r == '.':
			// A dot is an explicit label boundary. Do not carry a pending
			// separator into the next label.
			normalized.WriteByte('.')
			separatorPending = false

		case r >= 'A' && r <= 'Z':
			writePendingSeparator(&normalized, &separatorPending)
			normalized.WriteByte(byte(r + ('a' - 'A')))

		case (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9'):
			writePendingSeparator(&normalized, &separatorPending)
			normalized.WriteByte(byte(r))

		default:
			// Hyphens, underscores, whitespace, punctuation and every
			// non-ASCII rune are separators. They are not transliterated.
			if normalized.Len() > 0 {
				separatorPending = true
			}
		}
	}

	labels := strings.Split(normalized.String(), ".")
	validLabels := make([]string, 0, len(labels))

	for _, label := range labels {
		label = strings.Trim(label, "-")
		if label == "" {
			continue
		}

		label = truncateLabel(label)
		if label != "" {
			validLabels = append(validLabels, label)
		}
	}

	if len(validLabels) == 0 {
		return "", fmt.Errorf(
			"name %q contains no ASCII letters or digits after normalization",
			name,
		)
	}

	result := strings.Join(validLabels, ".")
	result = truncateSubdomain(result)

	if result == "" {
		return "", fmt.Errorf(
			"name %q is empty after applying DNS-subdomain limits",
			name,
		)
	}

	if validationErrors := k8svalidation.IsDNS1123Subdomain(result); len(validationErrors) > 0 {
		return "", fmt.Errorf(
			"name %q normalized to invalid DNS subdomain %q: %s",
			name,
			result,
			strings.Join(validationErrors, "; "),
		)
	}

	return result, nil
}

// writePendingSeparator emits one hyphen before the next alphanumeric
// character when one or more separator characters were observed.
//
// A separator is not emitted at the beginning of the complete name or
// immediately after an explicit dot boundary.
func writePendingSeparator(
	builder *strings.Builder,
	pending *bool,
) {
	if !*pending {
		return
	}

	value := builder.String()
	if value != "" &&
		value[len(value)-1] != '.' &&
		value[len(value)-1] != '-' {
		builder.WriteByte('-')
	}

	*pending = false
}

// truncateLabel limits one DNS label to 63 bytes and ensures that truncation
// does not leave a trailing hyphen.
func truncateLabel(label string) string {
	if len(label) > maxDNSLabelLength {
		label = label[:maxDNSLabelLength]
	}

	return strings.TrimRight(label, "-")
}

// truncateSubdomain limits the complete DNS subdomain to 253 bytes and ensures
// that truncation does not leave a trailing dot or hyphen.
//
// All output is ASCII, so byte truncation cannot split a UTF-8 sequence.
func truncateSubdomain(value string) string {
	if len(value) > maxDNSSubdomainLength {
		value = value[:maxDNSSubdomainLength]
	}

	return strings.TrimRight(value, ".-")
}

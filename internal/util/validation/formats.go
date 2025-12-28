package validation

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	Dns1123LabelFmt       string = `[a-z0-9]([-a-z0-9]*[a-z0-9])?`
	dns1123LabelMaxLength int    = 63
	DNS1123MaxLength      int    = 253
	envVarNameFmt         string = `[A-Za-z_][A-Za-z0-9_]*`

	// HostnameOrFQDNFmt validates a hostname or FQDN (Fully Qualified Domain Name).
	// A hostname is a single DNS label, an FQDN is one or more labels separated by dots.
	// Each label follows DNS rules: lowercase alphanumerics and hyphens, start and end with alphanumeric.
	// The final label must start with a letter (to reject IP addresses like 192.168.1.1).
	// Examples: "localhost", "my-host", "example.com", "api.example.com"
	hostnameOrFQDNLastLabelFmt string = `[a-z]([-a-z0-9]*[a-z0-9])?`
	HostnameOrFQDNFmt          string = `(` + Dns1123LabelFmt + `\.)*` + hostnameOrFQDNLastLabelFmt

	// HostnameOrFQDNWithOptionalPortFmt extends HostnameOrFQDNFmt with an optional port suffix.
	// Examples: "localhost", "quay.io", "registry.example.com:5000"
	portFmt                           string = `:[0-9]{1,5}`
	HostnameOrFQDNWithOptionalPortFmt string = HostnameOrFQDNFmt + `(` + portFmt + `)?`
	HostnameOrFQDNWithOptionalPortMax int    = DNS1123MaxLength + 6 // +6 for ":65535"

	// IP address patterns for registry URLs
	// IPv4: 192.168.1.1 or 192.168.1.1:5000
	ipv4Fmt string = `[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}`
	// IPv6 in brackets: [::1] or [2001:db8::1]:5000
	ipv6Fmt string = `\[[a-fA-F0-9:]+\]`

	// HostIPOrFQDNWithOptionalPortFmt allows hostname, FQDN, IPv4, or IPv6 with optional port
	// Examples: "localhost", "quay.io", "192.168.1.1:5000", "[::1]:5000"
	HostIPOrFQDNWithOptionalPortFmt string = `(` + HostnameOrFQDNFmt + `|` + ipv4Fmt + `|` + ipv6Fmt + `)(` + portFmt + `)?`
	HostIPOrFQDNWithOptionalPortMax int    = 269 // IPv6 max ~45 + brackets + port
)

var (
	GenericNameRegexp                    = regexp.MustCompile("^" + Dns1123LabelFmt + "$")
	EnvVarNameRegexp                     = regexp.MustCompile("^" + envVarNameFmt + "$")
	HostnameOrFQDNRegexp                 = regexp.MustCompile("^" + HostnameOrFQDNFmt + "$")
	HostnameOrFQDNWithOptionalPortRegexp = regexp.MustCompile("^" + HostnameOrFQDNWithOptionalPortFmt + "$")
	HostIPOrFQDNWithOptionalPortRegexp   = regexp.MustCompile("^" + HostIPOrFQDNWithOptionalPortFmt + "$")
)

func ValidateGenericName(name *string, path string) []error {
	return ValidateString(name, path, 1, dns1123LabelMaxLength, GenericNameRegexp, Dns1123LabelFmt)
}

func ValidateHostnameOrFQDN(name *string, path string) []error {
	errs := ValidateString(name, path, 1, DNS1123MaxLength, HostnameOrFQDNRegexp, HostnameOrFQDNFmt, "example.com")
	if name == nil || *name == "" {
		return errs
	}
	for _, label := range strings.Split(*name, ".") {
		if len(label) > dns1123LabelMaxLength {
			errs = append(errs, field.Invalid(fieldPathFor(path), label, fmt.Sprintf("must have at most %d characters", dns1123LabelMaxLength)))
			break
		}
	}
	return errs
}

// ValidateHostnameOrFQDNWithOptionalPort validates a hostname or FQDN with an optional port suffix.
// Examples: "localhost", "quay.io", "registry.example.com:5000"
func ValidateHostnameOrFQDNWithOptionalPort(name *string, path string) []error {
	errs := ValidateString(name, path, 1, HostnameOrFQDNWithOptionalPortMax, HostnameOrFQDNWithOptionalPortRegexp, HostnameOrFQDNWithOptionalPortFmt, "quay.io", "registry.example.com:5000")
	if name == nil || *name == "" {
		return errs
	}
	// Strip port before validating label lengths, and validate port range
	host := *name
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		portStr := host[idx+1:]
		host = host[:idx]
		// Validate port is in valid range (1-65535)
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, field.Invalid(fieldPathFor(path), *name, "port must be between 1 and 65535"))
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > dns1123LabelMaxLength {
			errs = append(errs, field.Invalid(fieldPathFor(path), label, fmt.Sprintf("must have at most %d characters", dns1123LabelMaxLength)))
			break
		}
	}
	return errs
}

// ValidateHostIPOrFQDNWithOptionalPort validates a hostname, FQDN, IPv4, or IPv6 address with optional port.
// Examples: "localhost", "quay.io", "192.168.1.1:5000", "[::1]:5000"
func ValidateHostIPOrFQDNWithOptionalPort(name *string, path string) []error {
	errs := ValidateString(name, path, 1, HostIPOrFQDNWithOptionalPortMax, HostIPOrFQDNWithOptionalPortRegexp, HostIPOrFQDNWithOptionalPortFmt, "quay.io", "192.168.1.1:5000", "[::1]:5000")
	if name == nil || *name == "" {
		return errs
	}

	value := *name
	var host, portStr string

	// Handle IPv6 in brackets: [::1]:5000
	if strings.HasPrefix(value, "[") {
		closeBracket := strings.Index(value, "]")
		if closeBracket == -1 {
			errs = append(errs, field.Invalid(fieldPathFor(path), value, "invalid IPv6 address format"))
			return errs
		}
		host = value[1:closeBracket] // Extract IP without brackets
		rest := value[closeBracket+1:]
		if strings.HasPrefix(rest, ":") {
			portStr = rest[1:]
		}
		// Validate IPv6
		if net.ParseIP(host) == nil {
			errs = append(errs, field.Invalid(fieldPathFor(path), value, "invalid IPv6 address"))
		}
	} else if idx := strings.LastIndex(value, ":"); idx != -1 && strings.Count(value, ":") == 1 {
		// IPv4 or hostname with port (single colon)
		host = value[:idx]
		portStr = value[idx+1:]
	} else {
		// No port
		host = value
	}

	// Validate port range if present
	if portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, field.Invalid(fieldPathFor(path), value, "port must be between 1 and 65535"))
		}
	}

	// Validate IPv4 if it looks like one
	if net.ParseIP(host) != nil {
		// Valid IP address, no further validation needed
		return errs
	}

	// Otherwise validate as hostname/FQDN - check label lengths
	for _, label := range strings.Split(host, ".") {
		if len(label) > dns1123LabelMaxLength {
			errs = append(errs, field.Invalid(fieldPathFor(path), label, fmt.Sprintf("must have at most %d characters", dns1123LabelMaxLength)))
			break
		}
	}
	return errs
}

const (
	// as per https://github.com/containers/image/blob/main/docker/reference/regexp.go
	ociDomainCompFmt           string = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
	ociNameCompFmt             string = `[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?`
	OciImageDomainFmt          string = ociDomainCompFmt + `(?:[.]` + ociDomainCompFmt + `)*` + `(?::[0-9]+)?`
	OciImageNameFmt            string = `(?:` + OciImageDomainFmt + `\/)?` + ociNameCompFmt + `(?:\/` + ociNameCompFmt + `)*`
	OciImageTagFmt             string = `[\w][\w.-]{0,127}`
	OciImageDigestFmt          string = `[A-Za-z][A-Za-z0-9]*(?:[-_+.][A-Za-z][A-Za-z0-9]*)*[:][[:xdigit:]]{32,}`
	OciImageReferenceFmt       string = `(` + OciImageNameFmt + `)(?:\:(` + OciImageTagFmt + `))?(?:\@(` + OciImageDigestFmt + `))?`
	OciImageReferenceMaxLength int    = 2048

	// short names (nginx:latest) are forbidden with strict mode
	StrictOciImageNameFmt      string = OciImageDomainFmt + `\/` + ociNameCompFmt + `(?:\/` + ociNameCompFmt + `)*`
	StrictOciImageReferenceFmt string = `(` + StrictOciImageNameFmt + `)(?:\:(` + OciImageTagFmt + `))?(?:\@(` + OciImageDigestFmt + `))?`

	// Template parameter pattern for Go templates
	templateParameterFmt = `\{\{[^}]*\}\}`
	// One or more template params as tag
	templatedTagFmt = `(?:` + templateParameterFmt + `)+`
	// OCI image reference with a templated tag and optional digest:
	// <name> ":" <templatedTag> [ "@" <digest> ]
	OciImageReferenceWithTemplatesFmt = `(` + OciImageNameFmt + `)(?:\:` + templatedTagFmt + `)(?:\@(` + OciImageDigestFmt + `))?`
)

// capture(namePat)
// optional(literal(":"), capture(tag))
// optional(literal("@"), capture(digestPat))

var (
	OciImageReferenceRegexp              = regexp.MustCompile("^" + OciImageReferenceFmt + "$")
	StrictOciImageReferenceRegexp        = regexp.MustCompile("^" + StrictOciImageReferenceFmt + "$")
	OciImageReferenceWithTemplatesRegexp = regexp.MustCompile("^" + OciImageReferenceWithTemplatesFmt + "$")
)

// Validates an OCI image reference.
func ValidateOciImageReference(s *string, path string) []error {
	return ValidateString(s, path, 1, OciImageReferenceMaxLength, OciImageReferenceRegexp, OciImageReferenceFmt, "quay.io/flightctl/flightctl:latest")
}

// Validates an OCI image reference in strict mode.
// This mode forbids short names (nginx:latest) and requires a domain name.
func ValidateOciImageReferenceStrict(s *string, path string) []error {
	return ValidateString(s, path, 1, OciImageReferenceMaxLength, StrictOciImageReferenceRegexp, StrictOciImageReferenceFmt, "quay.io/flightctl/flightctl:latest")
}

// Validates an OCI image reference that can contain template parameters.
func ValidateOciImageReferenceWithTemplates(s *string, path string) []error {
	return ValidateString(s, path, 1, OciImageReferenceMaxLength, OciImageReferenceWithTemplatesRegexp, OciImageReferenceWithTemplatesFmt, "quay.io/flightctl/device:{{ .metadata.labels.version }}")
}

const (
	// as per https://docs.github.com/en/get-started/using-git/dealing-with-special-characters-in-branch-and-tag-names#naming-branches-and-tags
	GitRevisionFmt string = `[a-zA-Z0-9]([a-zA-Z0-9\.\-\_\/])*`
	// GitHub limits to 255 minus "refs/heads/"
	GitRevisionMaxLength int = 244
)

var GitRevisionRegexp = regexp.MustCompile("^" + GitRevisionFmt + "$")

func ValidateGitRevision(name *string, path string) []error {
	return ValidateString(name, path, 1, GitRevisionMaxLength, GitRevisionRegexp, GitRevisionFmt)
}

const (
	// SystemD unit pattern supports all allowed formats for unit files and glob searches
	// This includes templated services (e.g., foo@.service, foo@bar.service)
	// and glob patterns (e.g., foo*.service, foo[0-9].service)
	SystemdNameFmt       string = `[0-9a-zA-Z:\-_.\\\[\]!\-\*\?]+(@[0-9a-zA-Z:\-_.\\\[\]!\-\*\?]+)?(\.[a-zA-Z\[\]!\-\*\?]+)?`
	SystemDNameMaxLength int    = 256 // SystemD unit names are limited to 256 characters
)

var SystemdNameRegexp = regexp.MustCompile("^" + SystemdNameFmt + "$")

func ValidateSystemdName(name *string, path string) []error {
	return ValidateString(name, path, 1, SystemDNameMaxLength, SystemdNameRegexp, SystemdNameFmt)
}

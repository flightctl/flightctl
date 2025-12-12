package validation

import (
	"fmt"
	"regexp"
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
)

var (
	GenericNameRegexp    = regexp.MustCompile("^" + Dns1123LabelFmt + "$")
	EnvVarNameRegexp     = regexp.MustCompile("^" + envVarNameFmt + "$")
	HostnameOrFQDNRegexp = regexp.MustCompile("^" + HostnameOrFQDNFmt + "$")
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

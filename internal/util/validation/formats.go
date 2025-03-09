package validation

import (
	"regexp"
)

const (
	Dns1123LabelFmt       string = `[a-z0-9]([-a-z0-9]*[a-z0-9])?`
	dns1123LabelMaxLength int    = 63
	DNS1123MaxLength      int    = 253
)

var GenericNameRegexp = regexp.MustCompile("^" + Dns1123LabelFmt + "$")

func ValidateGenericName(name *string, path string) []error {
	return ValidateString(name, path, 1, dns1123LabelMaxLength, GenericNameRegexp, Dns1123LabelFmt)
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
)

// capture(namePat)
// optional(literal(":"), capture(tag))
// optional(literal("@"), capture(digestPat))

var (
	OciImageReferenceRegexp = regexp.MustCompile("^" + OciImageReferenceFmt + "$")
)

// Validates an OCI image reference.
func ValidateOciImageReference(s *string, path string) []error {
	return ValidateString(s, path, 1, OciImageReferenceMaxLength, OciImageReferenceRegexp, OciImageReferenceFmt, "quay.io/flightctl/flightctl:latest")
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

package service

import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	// OCI image name component format as per https://github.com/containers/image/blob/main/docker/reference/regexp.go
	// Repository name components: lowercase alphanumeric with dots, underscores, and hyphens
	// Cannot start or end with a separator
	ociImageNameComponentFmt string = `[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?`
	// Full repository name: one or more components separated by forward slashes
	ociImageRepositoryNameFmt string = `(?:` + ociImageNameComponentFmt + `)(?:\/` + ociImageNameComponentFmt + `)*`
	// Maximum length for repository name (reasonable limit)
	ociImageRepositoryNameMaxLength int = 255

	// OCI image tag format as per https://github.com/containers/image/blob/main/docker/reference/regexp.go
	// Tag: must start with word character, then word characters, dots, or hyphens
	// Maximum length is 128 characters
	ociImageTagFmt       string = `[\w][\w.-]{0,127}`
	ociImageTagMaxLength int    = 128
)

var (
	ociImageRepositoryNameRegexp = regexp.MustCompile("^" + ociImageRepositoryNameFmt + "$")
	ociImageTagRegexp            = regexp.MustCompile("^" + ociImageTagFmt + "$")
)

// ValidateImageName validates an OCI image repository name according to RFC specifications.
// Repository names must:
// - Consist of lowercase alphanumeric characters
// - May contain dots, underscores, and hyphens as separators
// - Cannot start or end with a separator
// - Components are separated by forward slashes
func ValidateImageName(imageName *string, path string) []error {
	if imageName == nil || *imageName == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	var errs []error
	if len(*imageName) > ociImageRepositoryNameMaxLength {
		errs = append(errs, field.TooLong(fieldPathFor(path), imageName, ociImageRepositoryNameMaxLength))
	}
	if !ociImageRepositoryNameRegexp.MatchString(*imageName) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *imageName, "must match OCI repository name format: lowercase alphanumeric with dots, underscores, or hyphens, separated by forward slashes"))
	}
	return errs
}

// ValidateImageTag validates an OCI image tag according to RFC specifications.
// Tags must:
// - Start with a word character (letter, digit, or underscore)
// - May contain word characters, dots, and hyphens
// - Cannot start with a period or dash
// - Maximum length is 128 characters
func ValidateImageTag(imageTag *string, path string) []error {
	if imageTag == nil || *imageTag == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	var errs []error
	if len(*imageTag) > ociImageTagMaxLength {
		errs = append(errs, field.TooLong(fieldPathFor(path), imageTag, ociImageTagMaxLength))
	}
	if !ociImageTagRegexp.MatchString(*imageTag) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *imageTag, "must match OCI tag format: start with alphanumeric or underscore, may contain alphanumeric, dots, hyphens, or underscores, max 128 characters"))
	}
	return errs
}

// fieldPathFor creates a field path from a string path
func fieldPathFor(path string) *field.Path {
	fields := strings.Split(path, ".")
	return field.NewPath(fields[0], fields[1:]...)
}

package validation

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/crypto"
	k8sapivalidation "k8s.io/apimachinery/pkg/api/validation"
	k8smetav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	k8sutilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateResourceName validates that metadata.name is not empty and is a valid name in K8s.
func ValidateResourceName(name *string) []error {
	return ValidateResourceNameReference(name, "metadata.name")
}

// ValidateResourceRef validates that metadata.name is not empty and is a valid name in K8s.
func ValidateResourceNameReference(name *string, path string) []error {
	errs := field.ErrorList{}
	if name == nil {
		errs = append(errs, field.Required(fieldPathFor(path), ""))
	} else {
		for _, msg := range k8sapivalidation.NameIsDNSSubdomain(*name, false) {
			errs = append(errs, field.Invalid(fieldPathFor(path), *name, msg))
		}
	}
	return asErrors(errs)
}

// ValidateLabels validates that a set of labels are valid K8s labels.
func ValidateLabels(labels *map[string]string) []error {
	return ValidateLabelsWithPath(labels, "metadata.labels")
}

// ValidateLabelsWithPath validates that a set of labels are valid K8s labels, with fieldPath being the path to the label field.
func ValidateLabelsWithPath(labels *map[string]string, path string) []error {
	if labels == nil {
		return []error{}
	}
	errs := k8smetav1validation.ValidateLabels(*labels, fieldPathFor(path))
	return asErrors(errs)
}

// ValidateStringMap validates that the k,v elements in a map are correctly defined as a string.
func ValidateStringMap(m *map[string]string, path string, minLen int, maxLen int, patternRegexp *regexp.Regexp, patternFmt string, patternExample ...string) []error {
	allErrs := []error{}
	if m == nil {
		return allErrs
	}
	for k, v := range *m {
		key := k
		value := v
		allErrs = append(allErrs, ValidateString(&key, path, minLen, maxLen, patternRegexp, patternFmt, patternExample...)...)
		allErrs = append(allErrs, ValidateString(&value, path, minLen, maxLen, patternRegexp, patternFmt, patternExample...)...)
	}
	return allErrs
}

// ValidateAnnotations validates that a set of annotations are valid K8s annotations.
func ValidateAnnotations(annotations *map[string]string) []error {
	if annotations == nil {
		return []error{}
	}
	errs := k8sapivalidation.ValidateAnnotations(*annotations, fieldPathFor("metadata.annotations"))
	return asErrors(errs)
}

// ValidateString validates that a string has a length between minLen and maxLen, and matches the provided pattern.
func ValidateString(s *string, path string, minLen int, maxLen int, patternRegexp *regexp.Regexp, patternFmt string, patternExample ...string) []error {
	if s == nil {
		return []error{}
	}

	errs := field.ErrorList{}
	if len(*s) < minLen {
		if minLen == 1 {
			errs = append(errs, field.Required(fieldPathFor(path), ""))
		} else {
			errs = append(errs, field.Invalid(fieldPathFor(path), *s, fmt.Sprintf("must have at least %d characters", minLen)))
		}
	}
	if len(*s) > maxLen {
		errs = append(errs, field.TooLong(fieldPathFor(path), s, maxLen))
	}
	if patternRegexp != nil && !patternRegexp.MatchString(*s) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *s, k8sutilvalidation.RegexError("Invalidpattern", patternFmt, patternExample...)))
	}
	return asErrors(errs)
}

func ValidateFilePath(s *string, path string) []error {
	if s == nil {
		return []error{}
	}

	errs := field.ErrorList{}
	if len(*s) > 4096 {
		errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must be less than 4096 characters"))
	}
	if !filepath.IsAbs(*s) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must be an absolute path"))
	}
	if filepath.Clean(*s) != *s {
		errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must be clean (without consecutive separators, . or .. elements)"))
	}

	return asErrors(errs)
}

func ValidateFileOrDirectoryPath(s *string, path string) []error {
	if s == nil {
		return []error{}
	}
	cleanS := strings.TrimSuffix(*s, "/")
	return ValidateFilePath(&cleanS, path)
}

func ValidateLinuxUserGroup(s *string, path string) []error {
	if s == nil {
		return []error{}
	}

	errs := field.ErrorList{}

	// Fully numeric usernames are not allowed, so we assume a numeric username is an ID
	// Source: man 8 useradd (similar text in man 8 groupadd)
	//
	// > Usernames may contain only lower and upper case letters, digits, underscores,
	// > or dashes. They can end with a dollar sign. Dashes are not allowed at the
	// > beginning of the username.
	// > Fully numeric usernames and usernames . or .. are also disallowed. It is not
	// > recommended to use usernames beginning with . character as their home directories
	// > will be hidden in the ls output.
	// > Usernames may only be up to 32 characters long.

	isID := false
	id, err := strconv.ParseInt(*s, 10, 64)
	if err == nil {
		isID = true
	}

	if isID {
		// https://systemd.io/UIDS-GIDS/
		if id < 0 {
			errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must be a positive number (invalid user ID)"))
		} else if id >= 4294967295 {
			errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must be smaller than 4294967295 (invalid user ID)"))
		} else if id == 65535 {
			errs = append(errs, field.Invalid(fieldPathFor(path), *s, "must not be equal to 65535 (invalid user ID)"))
		}
		return asErrors(errs)
	}

	if len(*s) > 32 {
		errs = append(errs, field.TooLong(fieldPathFor(path), s, 32))
	}

	re := regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*[$]?$`)
	if !re.Match([]byte(*s)) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *s, "is not a valid user name"))
	}

	return asErrors(errs)
}

func ValidateLinuxFileMode(m *int, path string) []error {
	if m != nil && (*m < 0 || *m > 07777) {
		return asErrors(field.ErrorList{field.Invalid(fieldPathFor(path), *m, "is not a valid mode")})
	}

	return []error{}
}

func ValidateBase64Field(s string, path string, maxLen int) []error {
	errs := field.ErrorList{}

	if len(s) > maxLen {
		errs = append(errs, field.TooLong(fieldPathFor(path), s, maxLen))
	}
	_, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		errs = append(errs, field.Invalid(fieldPathFor(path), s, "must be a valid base64 encoded string"))
	}

	return asErrors(errs)
}

func ValidateBearerToken(token *string, path string) []error {
	if token == nil {
		return []error{}
	}

	// https://www.rfc-editor.org/info/rfc7519
	var jwtPattern = regexp.MustCompile(`^[A-Za-z0-9-_]+\.[A-Za-z0-9-_]+\.[A-Za-z0-9-_]+$`)

	errs := field.ErrorList{}
	if !jwtPattern.MatchString(*token) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *token, "must be a valid JWT token"))
	} else {
		parts := strings.Split(*token, ".")
		for i, part := range parts {
			if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
				errs = append(errs, field.Invalid(fieldPathFor(fmt.Sprintf("%s.part%d", path, i+1)), part, "must be a valid base64url encoded string"))
			}
		}
	}
	return asErrors(errs)
}

// Currently every request is sent to the only signer, named "ca" and defined in cmd/flightctl-api/main.go
func ValidateSignerName(s string) []error {
	errs := field.ErrorList{}

	validSigners := map[string]struct{}{
		"ca":         {}, // general signer
		"enrollment": {}, // special logic for enrollment certs, but afterwards fwds to same 'ca' signer internally
	}

	if _, exists := validSigners[s]; exists {
		return nil
	}

	msg := "must specify a valid signer: "
	for k := range validSigners {
		msg += k
	}
	errs = append(errs, field.Invalid(fieldPathFor("spec.signerName"), s, msg))
	return asErrors(errs)
}

// TODO: this should log a warning if less than minExpirationSeconds using the configured logger
func ValidateExpirationSeconds(e *int32) []error {
	return nil
}

func ValidateCSR(csr []byte) []error {
	errs := field.ErrorList{}

	_, err := crypto.ParseCSR(csr)
	if err != nil {
		errs = append(errs, field.Invalid(fieldPathFor("spec.request"), csr, err.Error()))
		return asErrors(errs)
	}
	return nil
}

func FormatInvalidError(input, path, errorMsg string) []error {
	errors := field.ErrorList{field.Invalid(fieldPathFor(path), input, errorMsg)}
	return asErrors(errors)
}

func fieldPathFor(path string) *field.Path {
	fields := strings.Split(path, ".")
	return field.NewPath(fields[0], fields[1:]...)
}

func asErrors(errs field.ErrorList) []error {
	agg := errs.ToAggregate()
	if agg == nil {
		return []error{}
	}
	return agg.Errors()
}

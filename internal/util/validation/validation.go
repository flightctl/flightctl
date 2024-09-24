package validation

import (
	"encoding/base64"
	"fmt"
	"regexp"
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

func ValidateCSRUsages(u *[]string) []error {
	errs := field.ErrorList{}
	requiredAllOf := map[string]struct{}{
		"clientAuth": {},
		"CA:false":   {},
	}
	notAllowed := map[string]struct{}{}

	for _, usage := range *u {
		if _, exists := notAllowed[usage]; exists {
			err := fmt.Sprintf("usage not allowed: %s\n", usage)
			errs = append(errs, field.Invalid(fieldPathFor("spec.usages"), u, err))
		}
		delete(requiredAllOf, usage)
	}

	l := len(requiredAllOf)
	if l > 0 {
		required := make([]string, l)
		for k := range requiredAllOf {
			required = append(required, k+", ")
		}
		err := fmt.Sprintf("required usages must be present in request: %s\n", required)
		errs = append(errs, field.Invalid(fieldPathFor("spec.usages"), u, err))
	}
	return asErrors(errs)
}

// Currently every request is sent to the only signer, named "ca" and defined in cmd/flightctl-api/main.go
func ValidateSignerName(s string) []error {
	errs := field.ErrorList{}

	validSigners := map[string]struct{}{
		"ca": {},
	}

	if _, exists := validSigners[s]; exists {
		return nil
	}

	errs = append(errs, field.Invalid(fieldPathFor("spec.signerName"), s, "must specify a valid signer"))
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

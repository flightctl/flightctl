package validation

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	k8sapivalidation "k8s.io/apimachinery/pkg/api/validation"
	k8smetav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	k8sutilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateName validates that metadata.name is not empty and is a valid name in K8s.
func ValidateResourceName(name *string) []error {
	errs := field.ErrorList{}
	if name == nil {
		errs = append(errs, field.Required(fieldPathFor("metadata.name"), ""))
	} else {
		for _, msg := range k8sapivalidation.NameIsDNSSubdomain(*name, false) {
			errs = append(errs, field.Invalid(fieldPathFor("metadata.name"), *name, msg))
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

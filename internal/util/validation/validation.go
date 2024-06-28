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

func IsBase64(s string) bool {
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
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

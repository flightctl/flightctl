package validation

import (
	"strings"

	k8sapivalidation "k8s.io/apimachinery/pkg/api/validation"
	k8smetav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateName validates that metadata.name is not empty and is a valid name in K8s.
func ValidateName(name *string) []error {
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

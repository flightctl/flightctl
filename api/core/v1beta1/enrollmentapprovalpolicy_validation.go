package v1beta1

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	EnrollmentApprovalPolicyKind       = "EnrollmentApprovalPolicy"
	EnrollmentApprovalPolicyAPIVersion = "v1beta1"
	EnrollmentApprovalPolicyListKind   = "EnrollmentApprovalPolicyList"
)

func (policy *EnrollmentApprovalPolicy) Validate() []error {
	if policy == nil {
		return []error{fmt.Errorf("policy is required")}
	}
	allErrors := validation.ValidateResourceName(policy.Metadata.Name)
	allErrors = append(allErrors, validation.ValidateLabels(policy.Metadata.Labels)...)
	allErrors = append(allErrors, validation.ValidateAnnotations(policy.Metadata.Annotations)...)
	allErrors = append(allErrors, validation.ValidateResourceNameReference(&policy.Spec.CertificateName, "spec.certificateName")...)
	allErrors = append(allErrors, validation.ValidateLabelsWithPath(&policy.Spec.DeviceLabels, "spec.deviceLabels")...)
	if policy.Spec.DeviceLabels == nil {
		allErrors = append(allErrors, fmt.Errorf("spec.deviceLabels is required"))
	}
	if strings.TrimSpace(policy.Spec.MatchExpression) == "" {
		allErrors = append(allErrors, fmt.Errorf("spec.matchExpression is required"))
	}
	if policy.Spec.AllowedIncomingLabels != nil {
		for i, key := range *policy.Spec.AllowedIncomingLabels {
			for _, detail := range validation.ValidateLabelKey(key) {
				allErrors = append(allErrors, field.Invalid(field.NewPath("spec", "allowedIncomingLabels").Index(i), key, detail))
			}
		}
	}
	if policy.Spec.ApprovalLimit != nil && *policy.Spec.ApprovalLimit < 0 {
		allErrors = append(allErrors, fmt.Errorf("spec.approvalLimit must be nonnegative"))
	}
	if policy.Spec.AutoApprovalWindowDays != nil && (*policy.Spec.AutoApprovalWindowDays < 1 || *policy.Spec.AutoApprovalWindowDays > 106751) {
		allErrors = append(allErrors, fmt.Errorf("spec.autoApprovalWindowDays must be between 1 and 106751"))
	}
	return allErrors
}

package v1alpha1

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/util/validation"
)

type Validator interface {
	Validate() []error
}

func (r Device) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	if r.Spec != nil {
		if r.Spec.Os != nil {
			allErrs = append(allErrs, validation.ValidateOciImageReference(&r.Spec.Os.Image, "spec.os.image")...)
		}
		if r.Spec.Config != nil {
			for _, config := range *r.Spec.Config {
				value, err := config.ValueByDiscriminator()
				if err != nil {
					allErrs = append(allErrs, fmt.Errorf("invalid configType: %s", err))
				} else {
					allErrs = append(allErrs, value.(Validator).Validate()...)
				}
			}
		}
		if r.Spec.Containers != nil {
			for i, matchPattern := range *r.Spec.Containers.MatchPatterns {
				matchPattern := matchPattern
				allErrs = append(allErrs, validation.ValidateString(&matchPattern, fmt.Sprintf("spec.containers.matchPatterns[%d]", i), 1, 256, nil, "")...)
			}
		}
		if r.Spec.Systemd != nil {
			for i, matchPattern := range *r.Spec.Systemd.MatchPatterns {
				matchPattern := matchPattern
				allErrs = append(allErrs, validation.ValidateString(&matchPattern, fmt.Sprintf("spec.systemd.matchPatterns[%d]", i), 1, 256, nil, "")...)
			}
		}
	}
	return allErrs
}

func (c GitConfigProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateGenericName(&c.GitRef.Repository, "spec.config[].gitRef.repository")...)
	allErrs = append(allErrs, validation.ValidateGitRevision(&c.GitRef.TargetRevision, "spec.config[].gitRef.targetRevision")...)
	allErrs = append(allErrs, validation.ValidateString(&c.GitRef.Path, "spec.config[].gitRef.path", 0, 2048, nil, "")...)
	return allErrs
}

func (c KubernetesSecretProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Name, "spec.config[].secretRef.name")...)
	allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Namespace, "spec.config[].secretRef.namespace")...)
	allErrs = append(allErrs, validation.ValidateString(&c.SecretRef.MountPath, "spec.config[].secretRef.mountPath", 0, 2048, nil, "")...)
	return allErrs
}

func (c InlineConfigProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	return allErrs
}

func (r EnrollmentRequest) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	return allErrs
}

func (r EnrollmentRequestApproval) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateLabelsWithPath(r.Labels, "labels")...)
	allErrs = append(allErrs, validation.ValidateString(r.ApprovedBy, "approvedBy", 0, 2048, nil, "")...)
	return allErrs
}

func (r Fleet) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	return allErrs
}

func (r Repository) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	repoSpec, err := r.Spec.GetGitGenericRepoSpec()
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid repository type: %s", err))
	} else {
		allErrs = append(allErrs, validation.ValidateString(&repoSpec.Repo, "spec.repo", 1, 2048, nil, "")...)
	}
	return allErrs
}

func (r ResourceSync) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, validation.ValidateGenericName(&r.Spec.Repository, "spec.repository")...)
	allErrs = append(allErrs, validation.ValidateGitRevision(&r.Spec.TargetRevision, "spec.targetRevision")...)
	allErrs = append(allErrs, validation.ValidateString(&r.Spec.Path, "spec.path", 0, 2048, nil, "")...)
	return allErrs
}

func (d *DeviceSystemInfo) IsEmpty() bool {
	return *d == DeviceSystemInfo{}
}

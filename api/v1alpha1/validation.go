package v1alpha1

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/util/validation"
)

const maxBase64CertificateLength = 20 * 1024 * 1024

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

	// Validate the Device spec settings
	if r.Spec.Template.Spec.Os != nil {
		allErrs = append(allErrs, validation.ValidateOciImageReference(&r.Spec.Template.Spec.Os.Image, "spec.template.spec.os.image")...)
	}

	if r.Spec.Template.Spec.Config != nil {
		for _, config := range *r.Spec.Template.Spec.Config {
			value, err := config.ValueByDiscriminator()
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("invalid configType: %s", err))
			} else {
				allErrs = append(allErrs, value.(Validator).Validate()...)
			}
		}
	}

	return allErrs
}

func (r Repository) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)

	// Validate GitGenericRepoSpec
	gitGenericRepoSpec, genericErr := r.Spec.GetGitGenericRepoSpec()
	if genericErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&gitGenericRepoSpec.Repo, "spec.repo", 1, 2048, nil, "")...)
	}

	// Validate GitHttpRepoSpec
	gitHttpRepoSpec, httpErr := r.Spec.GetGitHttpRepoSpec()
	if httpErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&gitHttpRepoSpec.Repo, "spec.repo", 1, 2048, nil, "")...)
		allErrs = append(allErrs, validateGitHttpConfig(&gitHttpRepoSpec.HttpConfig)...)
	}

	// Validate GitSshRepoSpec
	gitSshRepoSpec, sshErr := r.Spec.GetGitSshRepoSpec()
	if sshErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&gitSshRepoSpec.Repo, "spec.repo", 1, 2048, nil, "")...)
		allErrs = append(allErrs, validateGitSshConfig(&gitSshRepoSpec.SshConfig)...)
	}

	if genericErr != nil && httpErr != nil && sshErr != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid repository type: no valid spec found"))
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

func validateGitHttpConfig(config *GitHttpConfig) []error {
	var errs []error
	if config != nil {
		if config.CaCrt != nil {
			err := validation.ValidateBase64Field(*config.CaCrt, "spec.httpConfig.CaCrt", maxBase64CertificateLength)
			if err != nil {
				errs = append(errs, fmt.Errorf("spec.httpConfig.caCrt must be a valid base64 encoded string: %v", err))
			}
		}

		if (config.Username != nil && config.Password == nil) || (config.Username == nil && config.Password != nil) {
			errs = append(errs, fmt.Errorf("both username and password must be provided together"))
		}
		if (config.TlsCrt != nil && config.TlsKey == nil) || (config.TlsCrt == nil && config.TlsKey != nil) {
			errs = append(errs, fmt.Errorf("both tlsCrt and tlsKey must be provided together"))
		}

		if config.Username != nil && config.Password != nil {
			errs = append(errs, validation.ValidateString(config.Username, "spec.httpConfig.username", 1, 256, nil, "")...)
			errs = append(errs, validation.ValidateString(config.Password, "spec.httpConfig.password", 1, 256, nil, "")...)
		}

		if config.TlsCrt != nil && config.TlsKey != nil {
			errs = append(errs, validation.ValidateBase64Field(*config.TlsCrt, "spec.httpConfig.TlsCrt", maxBase64CertificateLength)...)
			errs = append(errs, validation.ValidateBase64Field(*config.TlsKey, "spec.httpConfig.TlsKey", maxBase64CertificateLength)...)
		}
	}
	return errs
}

func validateGitSshConfig(config *GitSshConfig) []error {
	var errs []error
	if config != nil {
		// Check if passphrase is specified without private key
		if config.PrivateKeyPassphrase != nil && config.SshPrivateKey == nil {
			errs = append(errs, fmt.Errorf("spec.sshConfig.privateKeyPassphrase cannot be specified without sshConfig.sshPrivateKey"))
		}

		if config.PrivateKeyPassphrase != nil {
			errs = append(errs, validation.ValidateString(config.PrivateKeyPassphrase, "spec.sshConfig.privateKeyPassphrase", 1, 256, nil, "")...)
		}
		if config.SshPrivateKey != nil {
			errs = append(errs, validation.ValidateBase64Field(*config.SshPrivateKey, "spec.sshConfig.SshPrivateKey", maxBase64CertificateLength)...)
		}
	}

	return errs
}

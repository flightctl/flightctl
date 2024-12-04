package v1alpha1

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
)

const maxBase64CertificateLength = 20 * 1024 * 1024
const maxInlineConfigLength = 1024 * 1024

var ErrStartGraceDurationExceedsCronInterval = errors.New("startGraceDuration exceeds the cron interval between schedule times")

type Validator interface {
	Validate() []error
}

func (r Device) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	if r.Spec != nil {
		if r.Spec.UpdatePolicy != nil {
			allErrs = append(allErrs, r.Spec.UpdatePolicy.Validate()...)
		}
		if r.Spec.Os != nil {
			allErrs = append(allErrs, validation.ValidateOciImageReference(&r.Spec.Os.Image, "spec.os.image")...)
		}
		if r.Spec.Config != nil {
			for _, config := range *r.Spec.Config {
				allErrs = append(allErrs, config.Validate()...)
			}
		}
		if r.Spec.Applications != nil {
			allErrs = append(allErrs, validateApplications(*r.Spec.Applications)...)
		}
		if r.Spec.Resources != nil {
			for _, resource := range *r.Spec.Resources {
				allErrs = append(allErrs, resource.Validate()...)
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

func (c ConfigProviderSpec) Validate() []error {
	allErrs := []error{}

	// validate the config provider type
	t, err := c.Type()
	if err != nil {
		allErrs = append(allErrs, err)
		return allErrs
	}

	switch t {
	case GitConfigProviderType:
		provider, err := c.AsGitConfigProviderSpec()
		if err != nil {
			allErrs = append(allErrs, err)
			break
		}
		allErrs = append(allErrs, provider.Validate()...)
	case HttpConfigProviderType:
		provider, err := c.AsHttpConfigProviderSpec()
		if err != nil {
			allErrs = append(allErrs, err)
			break
		}
		allErrs = append(allErrs, provider.Validate()...)
	case InlineConfigProviderType:
		provider, err := c.AsInlineConfigProviderSpec()
		if err != nil {
			allErrs = append(allErrs, err)
			break
		}
		allErrs = append(allErrs, provider.Validate()...)
	case KubernetesSecretProviderType:
		provider, err := c.AsKubernetesSecretProviderSpec()
		if err != nil {
			allErrs = append(allErrs, err)
			break
		}
		allErrs = append(allErrs, provider.Validate()...)
	default:
		// if we hit this case, it means that the type should be added to the switch statement above
		allErrs = append(allErrs, fmt.Errorf("unknown config provider type: %s", t))
	}

	return allErrs
}

func (a HookAction) Validate(path string) []error {
	allErrs := []error{}

	t, err := a.Type()
	if err != nil {
		allErrs = append(allErrs, err)
		return allErrs
	}

	switch t {
	case HookActionTypeRun:
		runAction, err := a.AsHookActionRun()
		if err != nil {
			allErrs = append(allErrs, err)
			return allErrs
		}
		allErrs = append(allErrs, validation.ValidateString(&runAction.Run, path+".run", 1, 2048, nil, "")...)
		// TODO: pull the extra validation done by the agent up here
		allErrs = append(allErrs, validation.ValidateStringMap(runAction.EnvVars, path+".envVars", 1, 256, nil, "")...)
		allErrs = append(allErrs, validation.ValidateFileOrDirectoryPath(runAction.WorkDir, path+".workDir")...)
	default:
		// if we hit this case, it means that the type should be added to the switch statement above
		allErrs = append(allErrs, fmt.Errorf("%s: unknown hook action type: %s", path, t))
	}

	if a.If != nil {
		for i, condition := range *a.If {
			allErrs = append(allErrs, condition.Validate(fmt.Sprintf("%s.if[%d]", path, i))...)
		}
	}

	return allErrs
}

func (c HookCondition) Validate(path string) []error {
	allErrs := []error{}

	t, err := c.Type()
	if err != nil {
		allErrs = append(allErrs, err)
		return allErrs
	}

	switch t {
	case HookConditionTypeExpression:
		expression, err := c.AsHookConditionExpression()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validation.ValidateString(&expression, path, 1, 2048, nil, "")...)
	case HookConditionTypePathOp:
		pathOpCondition, err := c.AsHookConditionPathOp()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validation.ValidateFileOrDirectoryPath(&pathOpCondition.Path, path+".path")...)
	default:
		// if we hit this case, it means that the type should be added to the switch statement above
		allErrs = append(allErrs, fmt.Errorf("%s: unknown hook condition type: %s", path, t))
	}

	return allErrs
}

func (r ResourceMonitor) Validate() []error {
	allErrs := []error{}

	monitorType, err := r.Discriminator()
	if err != nil {
		allErrs = append(allErrs, err)
	}

	validateAlertRulesFn := func(alertRules []ResourceAlertRule, samplingInterval string) []error {
		seen := make(map[string]struct{})
		for _, rule := range alertRules {
			// ensure uniqueness of Severity per resource type
			if _, exists := seen[string(rule.Severity)]; exists {
				allErrs = append(allErrs, fmt.Errorf("duplicate alertRule severity: %s", rule.Severity))
			} else {
				seen[string(rule.Severity)] = struct{}{}
			}
			allErrs = append(allErrs, rule.Validate(samplingInterval)...)
		}
		return allErrs
	}

	switch monitorType {
	case "CPU":
		spec, err := r.AsCPUResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validateAlertRulesFn(spec.AlertRules, spec.SamplingInterval)...)
	case "Disk":
		spec, err := r.AsDiskResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validation.ValidateString(&spec.Path, "spec.resources[].disk.path", 0, 2048, nil, "")...)
		allErrs = append(allErrs, validateAlertRulesFn(spec.AlertRules, spec.SamplingInterval)...)
	case "Memory":
		spec, err := r.AsMemoryResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validateAlertRulesFn(spec.AlertRules, spec.SamplingInterval)...)
	default:
		allErrs = append(allErrs, fmt.Errorf("unknown monitor type valid types are CPU, Disk and Memory: %s", monitorType))
	}

	return allErrs
}

func (r ResourceAlertRule) Validate(specSampleInterval string) []error {
	allErrs := []error{}

	sampleInterval, err := time.ParseDuration(specSampleInterval)
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid sampling interval: %s", err))
	}
	if r.Percentage < 0 || r.Percentage > 100 {
		allErrs = append(allErrs, fmt.Errorf("percentage must be between 0 and 100: %v", r.Percentage))
	}
	durationInterval, err := time.ParseDuration(r.Duration)
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid duration: %s", err))
	}
	if sampleInterval >= durationInterval {
		allErrs = append(allErrs, fmt.Errorf("sampling interval %s must be less than the duration: %s", sampleInterval.String(), durationInterval.String()))
	}
	if r.Description != "" {
		allErrs = append(allErrs, validation.ValidateString(&r.Description, "spec.resources[].alertRules.description", 1, 256, nil, "")...)
	}
	return allErrs
}

func (c GitConfigProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateResourceNameReference(&c.GitRef.Repository, "spec.config[].gitRef.repository")...)
	allErrs = append(allErrs, validation.ValidateString(&c.GitRef.TargetRevision, "spec.config[].gitRef.targetRevision", 0, 1024, nil, "")...)
	allErrs = append(allErrs, validation.ValidateString(&c.GitRef.Path, "spec.config[].gitRef.path", 0, 2048, nil, "")...)
	return allErrs
}

func (c KubernetesSecretProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Name, "spec.config[].secretRef.name")...)
	allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Namespace, "spec.config[].secretRef.namespace")...)
	allErrs = append(allErrs, validation.ValidateFilePath(&c.SecretRef.MountPath, "spec.config[].secretRef.mountPath")...)

	return allErrs
}

func (c InlineConfigProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	for i := range c.Inline {
		allErrs = append(allErrs, validation.ValidateFilePath(&c.Inline[i].Path, fmt.Sprintf("spec.config[].inline[%d].path", i))...)
		allErrs = append(allErrs, validation.ValidateLinuxUserGroup(c.Inline[i].User, fmt.Sprintf("spec.config[].inline[%d].user", i))...)
		allErrs = append(allErrs, validation.ValidateLinuxUserGroup(c.Inline[i].Group, fmt.Sprintf("spec.config[].inline[%d].group", i))...)
		allErrs = append(allErrs, validation.ValidateLinuxFileMode(c.Inline[i].Mode, fmt.Sprintf("spec.config[].inline[%d].mode", i))...)

		if c.Inline[i].ContentEncoding != nil && *(c.Inline[i].ContentEncoding) == Base64 {
			// Contents should be base64 encoded and limited to 1MB (1024*1024=1048576 bytes)
			allErrs = append(allErrs, validation.ValidateBase64Field(c.Inline[i].Content, fmt.Sprintf("spec.config[].inline[%d].content", i), maxInlineConfigLength)...)
		} else if c.Inline[i].ContentEncoding == nil || (c.Inline[i].ContentEncoding != nil && *(c.Inline[i].ContentEncoding) == Base64) {
			// Contents should be limited to 1MB (1024*1024=1048576 bytes)
			allErrs = append(allErrs, validation.ValidateString(&c.Inline[i].Content, fmt.Sprintf("spec.config[].inline[%d].content", i), 0, maxInlineConfigLength, nil, "")...)
		} else {
			allErrs = append(allErrs, fmt.Errorf("unknown contentEncoding: %s", *(c.Inline[i].ContentEncoding)))
		}
	}
	return allErrs
}

func (h HttpConfigProviderSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&h.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateResourceNameReference(&h.HttpRef.Repository, "spec.config[].httpRef.repository")...)
	allErrs = append(allErrs, validation.ValidateFilePath(&h.HttpRef.FilePath, "spec.config[].httpRef.filePath")...)
	allErrs = append(allErrs, validation.ValidateString(h.HttpRef.Suffix, "spec.config[].httpRef.suffix", 0, 2048, nil, "")...)

	return allErrs
}

func (r EnrollmentRequest) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, validation.ValidateCSR([]byte(r.Spec.Csr))...)
	return allErrs
}

func (r EnrollmentRequestApproval) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateLabelsWithPath(r.Labels, "labels")...)
	allErrs = append(allErrs, validation.ValidateString(r.ApprovedBy, "approvedBy", 0, 2048, nil, "")...)
	return allErrs
}

func (r CertificateSigningRequest) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, validation.ValidateCSRUsages(r.Spec.Usages)...)
	allErrs = append(allErrs, validation.ValidateExpirationSeconds(r.Spec.ExpirationSeconds)...)
	allErrs = append(allErrs, validation.ValidateSignerName(r.Spec.SignerName)...)
	allErrs = append(allErrs, validation.ValidateCSR(r.Spec.Request)...)
	return allErrs
}

func (r Fleet) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, r.Spec.Selector.Validate()...)
	if r.Spec.RolloutPolicy != nil {
		i, err := r.Spec.RolloutPolicy.DeviceSelection.ValueByDiscriminator()
		if err != nil {
			allErrs = append(allErrs, err)
		} else {
			switch v := i.(type) {
			case BatchSequence:
				for _, b := range lo.FromPtr(v.Sequence) {
					allErrs = append(allErrs, b.Selector.Validate()...)
				}
			}
		}
	}

	// Validate the Device spec settings
	if r.Spec.Template.Spec.UpdatePolicy != nil {
		allErrs = append(allErrs, r.Spec.Template.Spec.UpdatePolicy.Validate()...)
	}

	if r.Spec.Template.Spec.Os != nil {
		allErrs = append(allErrs, validation.ValidateOciImageReference(&r.Spec.Template.Spec.Os.Image, "spec.template.spec.os.image")...)
	}

	if r.Spec.Template.Spec.Applications != nil {
		allErrs = append(allErrs, validateApplications(*r.Spec.Template.Spec.Applications)...)
	}

	if r.Spec.Template.Spec.Resources != nil {
		for _, resource := range *r.Spec.Template.Spec.Resources {
			allErrs = append(allErrs, resource.Validate()...)
		}
	}

	if r.Spec.Template.Spec.Config != nil {
		for _, config := range *r.Spec.Template.Spec.Config {
			allErrs = append(allErrs, config.Validate()...)
		}
	}

	return allErrs
}

func (u DeviceUpdatePolicySpec) Validate() []error {
	allErrs := []error{}
	if u.DownloadSchedule != nil {
		if err := u.DownloadSchedule.Validate(); err != nil {
			allErrs = append(allErrs, err...)
		}
	}
	if u.UpdateSchedule != nil {
		if err := u.UpdateSchedule.Validate(); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	return allErrs
}

func (u UpdateSchedule) Validate() []error {
	var allErrs []error
	if u.TimeZone != nil {
		if err := validateTimeZone(util.FromPtr(u.TimeZone)); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	// allow only the standard 5 input cron syntax e.g. "* * * * *"
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(u.At)
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid cron schedule: %s", err))
	}

	if u.StartGraceDuration != nil {
		if err := validateGraceDuration(schedule, *u.StartGraceDuration); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return allErrs
}

func (r Repository) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)

	// Validate GenericRepoSpec
	genericRepoSpec, genericErr := r.Spec.GetGenericRepoSpec()
	if genericErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&genericRepoSpec.Url, "spec.url", 1, 2048, nil, "")...)
	}

	// Validate HttpRepoSpec
	httpRepoSpec, httpErr := r.Spec.GetHttpRepoSpec()
	if httpErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&httpRepoSpec.Url, "spec.url", 1, 2048, nil, "")...)
		allErrs = append(allErrs, validateHttpConfig(&httpRepoSpec.HttpConfig)...)
	}

	// Validate SshRepoSpec
	sshRepoSpec, sshErr := r.Spec.GetSshRepoSpec()
	if sshErr == nil {
		allErrs = append(allErrs, validation.ValidateString(&sshRepoSpec.Url, "spec.url", 1, 2048, nil, "")...)
		allErrs = append(allErrs, validateSshConfig(&sshRepoSpec.SshConfig)...)
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
	allErrs = append(allErrs, validation.ValidateResourceNameReference(&r.Spec.Repository, "spec.repository")...)
	allErrs = append(allErrs, validation.ValidateGitRevision(&r.Spec.TargetRevision, "spec.targetRevision")...)
	allErrs = append(allErrs, validation.ValidateString(&r.Spec.Path, "spec.path", 0, 2048, nil, "")...)
	return allErrs
}

func (l *LabelSelector) Validate() []error {
	if l != nil && l.MatchExpressions == nil && l.MatchLabels == nil {
		return []error{errors.New("at least one of [matchLabels,matchExpressions] must appear in a label selector")}
	}
	return nil
}

func (d *DeviceSystemInfo) IsEmpty() bool {
	return *d == DeviceSystemInfo{}
}

func validateHttpConfig(config *HttpConfig) []error {
	var errs []error
	if config != nil {
		if config.CaCrt != nil {
			errs = append(errs, validation.ValidateBase64Field(*config.CaCrt, "spec.httpConfig.CaCrt", maxBase64CertificateLength)...)
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

		if config.Token != nil {
			errs = append(errs, validation.ValidateBearerToken(config.Token, "spec.httpConfig.token")...)
		}
	}
	return errs
}

func validateSshConfig(config *SshConfig) []error {
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

func (a ApplicationSpec) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateString(a.Name, "spec.applications[].name", 1, 256, nil, "")...)
	allErrs = append(allErrs, validation.ValidateStringMap(a.EnvVars, "spec.applications[].envVars", 1, 256, nil, "")...)
	return allErrs
}

func validateApplications(apps []ApplicationSpec) []error {
	allErrs := []error{}
	seenName := make(map[string]struct{})
	for _, app := range apps {
		providerType, err := validateAppProviderType(app)
		if err != nil {
			allErrs = append(allErrs, err)
		}
		appName, err := getAppName(app, providerType)
		if err != nil {
			allErrs = append(allErrs, err)
		}

		// ensure uniqueness of application name
		if _, exists := seenName[appName]; exists {
			allErrs = append(allErrs, fmt.Errorf("duplicate application name: %s", appName))
		} else {
			seenName[appName] = struct{}{}
		}

		allErrs = append(allErrs, validateAppProvider(app, providerType)...)
		allErrs = append(allErrs, app.Validate()...)
	}
	return allErrs
}

func validateAppProvider(app ApplicationSpec, appType ApplicationProviderType) []error {
	var errs []error
	switch appType {
	case ImageApplicationProviderType:
		provider, err := app.AsImageApplicationProvider()
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid image application provider: %w", err))
			return errs
		}

		if provider.Image == "" && app.Name == nil {
			errs = append(errs, fmt.Errorf("image reference cannot be empty when application name is not provided"))
		} else if app.Name != nil {
			errs = append(errs, validation.ValidateOciImageReference(&provider.Image, "spec.applications[].image")...)
		}
	default:
		errs = append(errs, fmt.Errorf("no validations implemented for application provider type: %s", appType))
	}

	return errs
}

func validateAppProviderType(app ApplicationSpec) (ApplicationProviderType, error) {
	providerType, err := app.Type()
	if err != nil {
		return "", fmt.Errorf("application type error: %w", err)
	}

	switch providerType {
	case ImageApplicationProviderType:
		return providerType, nil
	default:
		return "", fmt.Errorf("unknown application provider type: %s", providerType)
	}
}

func getAppName(app ApplicationSpec, appType ApplicationProviderType) (string, error) {
	switch appType {
	case ImageApplicationProviderType:
		provider, err := app.AsImageApplicationProvider()
		if err != nil {
			return "", fmt.Errorf("invalid image application provider: %w", err)
		}

		// default name to provider image if not provided
		if app.Name == nil {
			if provider.Image == "" {
				return "", fmt.Errorf("provider image cannot be empty when application name is not provided")
			}
			return provider.Image, nil
		}

		return *app.Name, nil
	default:
		return "", fmt.Errorf("unsupported application provider type: %s", appType)
	}
}

// note: this regex was taken from the github.com/kubernetes/kubernetes/pkg/apis/batch/validation/validation.go
// https://data.iana.org/time-zones/theory.html#naming
// * A name must not be empty, or contain '//', or start or end with '/'.
// * Do not use the file name components '.' and '..'.
// * Within a file name component, use only ASCII letters, '.', '-' and '_'.
// * Do not use digits, as that might create an ambiguity with POSIX TZ strings.
// * A file name component must not exceed 14 characters or start with '-'
//
// 0-9 and + characters are tolerated to accommodate legacy compatibility names
var validTimeZoneCharacters = regexp.MustCompile(`^[A-Za-z\.\-_0-9+]{1,14}$`)

// validateTimeZone validates the time zone string. it must be a valid IANA time zone identifier.
func validateTimeZone(timeZone string) []error {
	allErrs := []error{}

	if len(timeZone) == 0 {
		allErrs = append(allErrs, fmt.Errorf("time zone must not be empty"))
		return allErrs
	}

	// the default time zone is "Local"
	if strings.EqualFold(timeZone, "Local") {
		return allErrs
	}

	// we only support non ambiguous time zones with 3 characters
	validThreeLetterZones := map[string]bool{
		"GMT": true,
		"UTC": true,
	}

	if len(timeZone) == 3 {
		if _, ok := validThreeLetterZones[timeZone]; !ok {
			allErrs = append(allErrs, fmt.Errorf("invalid time zone: %s", timeZone))
		}
		return allErrs
	}

	// split the time zone identifier into parts
	parts := strings.Split(timeZone, "/")
	for _, part := range parts {
		if part == "." || part == ".." || strings.HasPrefix(part, "-") || !validTimeZoneCharacters.MatchString(part) {
			allErrs = append(allErrs, fmt.Errorf("invalid time zone component: %s", part))
			return allErrs
		}
	}

	if _, err := time.LoadLocation(timeZone); err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid time zone: %s: %w", timeZone, err))
	}

	return allErrs
}

func validateGraceDuration(schedule cron.Schedule, duration string) error {
	graceDuration, err := time.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}

	start := time.Now()
	var cronTimes []time.Time
	// test through the next 5 cron times
	for i := 0; i < 5; i++ {
		start = schedule.Next(start)
		cronTimes = append(cronTimes, start)
	}

	// Calculate the minimum interval between cron times
	for i := 1; i < len(cronTimes); i++ {
		interval := cronTimes[i].Sub(cronTimes[i-1])
		if graceDuration > interval {
			return fmt.Errorf("%w: %s", ErrStartGraceDurationExceedsCronInterval, graceDuration)
		}
	}

	return nil
}

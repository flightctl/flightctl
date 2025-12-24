package v1beta1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
)

const (
	maxBase64CertificateLength  = 20 * 1024 * 1024
	maxInlineLength             = 1024 * 1024
	privilegedPortRangeStart    = 1
	nonPrivilegedPortRangeStart = 1024
	portRangeEnd                = 65535
)

var (
	ErrStartGraceDurationExceedsCronInterval = errors.New("startGraceDuration exceeds the cron interval between schedule times")
	ErrInfoAlertLessThanWarn                 = errors.New("info alert percentage must be less than warning")
	ErrInfoAlertLessThanCritical             = errors.New("info alert percentage must be less than critical")
	ErrWarnAlertLessThanCritical             = errors.New("warning alert percentage must be less than critical")
	ErrDuplicateAlertSeverity                = errors.New("duplicate alertRule severity")
	ErrDuplicateMonitorType                  = errors.New("duplicate monitorType in resources")
	ErrInvalidCPUMonitorField                = errors.New("invalid field for CPU monitor")
	ErrInvalidMemoryMonitorField             = errors.New("invalid field for Memory monitor")
	ErrClaimPathRequiredDynamicOrg           = errors.New("claimPath is required for dynamic assignment")
	ErrClaimPathRequiredDynamicRole          = errors.New("claimPath is required for dynamic role assignment")
	ErrMappedIdentityNotFound                = errors.New("mapped identity not found in context")
	ErrInvalidMappedIdentityType             = errors.New("invalid mapped identity type in context")
	ErrIssuerRequired                        = errors.New("issuer is required")
	ErrClientIdRequired                      = errors.New("clientId is required")
	ErrAuthorizationUrlRequired              = errors.New("authorizationUrl is required")
	ErrTokenUrlRequired                      = errors.New("tokenUrl is required")
	ErrUserinfoUrlRequired                   = errors.New("userinfoUrl is required")
	ErrOrganizationNameRequired              = errors.New("organizationName is required for static assignment")
	ErrRolesRequired                         = errors.New("at least one role is required for static role assignment")
	ErrK8sProviderConfigOnly                 = errors.New("k8s provider type can only be created from configuration, not via API")
	ErrAapProviderConfigOnly                 = errors.New("aap provider type can only be created from configuration, not via API")
	ErrDynamicOrgMappingAdminOnly            = errors.New("only flightctl-admin users are allowed to create auth providers with dynamic organization mapping")
	ErrPerUserOrgMappingAdminOnly            = errors.New("only flightctl-admin users are allowed to create auth providers with per-user organization mapping")
	ErrStaticRoleMappingAdminOnly            = errors.New("only flightctl-admin users are allowed to create static role mappings for flightctl-admin")
)

type Validator interface {
	Validate() []error
}

func (d Device) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(d.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(d.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(d.Metadata.Annotations)...)
	if d.Spec != nil {
		allErrs = append(allErrs, d.Spec.Validate(false)...)
	}

	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged for Device.
func (d *Device) ValidateUpdate(newObj *Device) []error {
	return validateImmutableCoreFields(d.Metadata.Name, newObj.Metadata.Name,
		d.ApiVersion, newObj.ApiVersion,
		d.Kind, newObj.Kind,
		d.Status, newObj.Status)
}

func (r DeviceSpec) Validate(fleetTemplate bool) []error {
	allErrs := []error{}
	if r.UpdatePolicy != nil {
		allErrs = append(allErrs, r.UpdatePolicy.Validate()...)
	}
	if r.Consoles != nil {
		allErrs = append(allErrs, fmt.Errorf("consoles are not supported through this api"))
	}
	if r.Os != nil {
		allErrs = append(allErrs, validateOciImageReference(&r.Os.Image, "spec.os.image", fleetTemplate)...)
	}
	if r.Config != nil {
		allErrs = append(allErrs, validateConfigs(*r.Config, fleetTemplate)...)
	}
	if r.Applications != nil {
		allErrs = append(allErrs, validateApplications(*r.Applications, fleetTemplate)...)
	}
	if r.Resources != nil {
		// Individual resource validation
		for _, resource := range *r.Resources {
			allErrs = append(allErrs, resource.Validate()...)
		}

		// Cross-resource validation
		allErrs = append(allErrs, validateResourceMonitor(*r.Resources)...)
	}
	if r.Systemd != nil && r.Systemd.MatchPatterns != nil {
		for i, matchPattern := range *r.Systemd.MatchPatterns {
			allErrs = append(allErrs, validation.ValidateSystemdName(&matchPattern, fmt.Sprintf("spec.systemd.matchPatterns[%d]", i))...)
		}
	}
	return allErrs
}

func validateConfigs(configs []ConfigProviderSpec, fleetTemplate bool) []error {
	allErrs := []error{}
	seenPath := make(map[string]struct{}, len(configs))
	for i, config := range configs {
		t, err := config.Type()
		if err != nil {
			allErrs = append(allErrs, err)
			return allErrs
		}

		switch t {
		case GitConfigProviderType:
			provider, err := config.AsGitConfigProviderSpec()
			if err != nil {
				allErrs = append(allErrs, err)
				break
			}
			allErrs = append(allErrs, provider.Validate(fleetTemplate)...)
		case HttpConfigProviderType:
			provider, err := config.AsHttpConfigProviderSpec()
			if err != nil {
				allErrs = append(allErrs, err)
				break
			}
			path := provider.HttpRef.FilePath
			if _, exists := seenPath[path]; exists {
				allErrs = append(allErrs, fmt.Errorf("spec.config[%d].httpRef, device path must be unique for all config providers: %s", i, path))
			} else {
				seenPath[path] = struct{}{}
			}
			allErrs = append(allErrs, provider.Validate(fleetTemplate)...)
		case InlineConfigProviderType:
			provider, err := config.AsInlineConfigProviderSpec()
			if err != nil {
				allErrs = append(allErrs, err)
				break
			}

			for j, inline := range provider.Inline {
				path := inline.Path
				if _, exists := seenPath[path]; exists {
					allErrs = append(allErrs, fmt.Errorf("spec.config[%d].inline[%d], device path must be unique for all config providers: %s", i, j, path))
				} else {
					seenPath[path] = struct{}{}
				}
			}
			allErrs = append(allErrs, provider.Validate(fleetTemplate)...)
		case KubernetesSecretProviderType:
			provider, err := config.AsKubernetesSecretProviderSpec()
			if err != nil {
				allErrs = append(allErrs, err)
				break
			}
			allErrs = append(allErrs, provider.Validate(fleetTemplate)...)
		default:
			// if we hit this case, it means that the type should be added to the switch statement above
			allErrs = append(allErrs, fmt.Errorf("unknown config provider type: %s", t))
		}
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
		allErrs = append(allErrs, validation.ValidateStringMap(runAction.EnvVars, path+".envVars", 1, 256, nil, nil, "")...)
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

	switch monitorType {
	case "CPU":
		spec, err := r.AsCpuResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		// CPU monitors should not have a path field
		if hasPathField(r.union) {
			allErrs = append(allErrs, fmt.Errorf("%w: CPU monitors cannot have a path field", ErrInvalidCPUMonitorField))
		}
		allErrs = append(allErrs, validateAlertRules(spec.AlertRules, spec.SamplingInterval)...)
	case "Disk":
		spec, err := r.AsDiskResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		allErrs = append(allErrs, validation.ValidateString(&spec.Path, "spec.resources[].disk.path", 0, 2048, nil, "")...)
		allErrs = append(allErrs, validateAlertRules(spec.AlertRules, spec.SamplingInterval)...)
	case "Memory":
		spec, err := r.AsMemoryResourceMonitorSpec()
		if err != nil {
			allErrs = append(allErrs, err)
		}
		// Memory monitors should not have a path field
		if hasPathField(r.union) {
			allErrs = append(allErrs, fmt.Errorf("%w: Memory monitors cannot have a path field", ErrInvalidMemoryMonitorField))
		}
		allErrs = append(allErrs, validateAlertRules(spec.AlertRules, spec.SamplingInterval)...)
	default:
		allErrs = append(allErrs, fmt.Errorf("unknown monitor type valid types are CPU, Disk and Memory: %s", monitorType))
	}

	return allErrs
}

func validateAlertRules(alertRules []ResourceAlertRule, samplingInterval string) []error {
	var allErrs []error
	seen := make(map[ResourceAlertSeverityType]struct{})
	percentages := make(map[ResourceAlertSeverityType]float32)

	for _, rule := range alertRules {
		if _, exists := seen[rule.Severity]; exists {
			allErrs = append(allErrs, fmt.Errorf("%w: %s", ErrDuplicateAlertSeverity, rule.Severity))
			continue
		}
		seen[rule.Severity] = struct{}{}
		percentages[rule.Severity] = rule.Percentage
		allErrs = append(allErrs, rule.Validate(samplingInterval)...)
	}

	info, hasInfo := percentages[ResourceAlertSeverityTypeInfo]
	warning, hasWarning := percentages[ResourceAlertSeverityTypeWarning]
	critical, hasCritical := percentages[ResourceAlertSeverityTypeCritical]

	if hasInfo && hasWarning && info >= warning {
		allErrs = append(allErrs, ErrInfoAlertLessThanWarn)
	}
	if hasInfo && hasCritical && info >= critical {
		allErrs = append(allErrs, ErrInfoAlertLessThanCritical)
	}
	if hasWarning && hasCritical && warning >= critical {
		allErrs = append(allErrs, ErrWarnAlertLessThanCritical)
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

func (c GitConfigProviderSpec) Validate(fleetTemplate bool) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateResourceNameReference(&c.GitRef.Repository, "spec.config[].gitRef.repository")...)

	containsParams, paramErrs := validateParametersInString(&c.GitRef.TargetRevision, "spec.config[].gitRef.targetRevision", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateString(&c.GitRef.TargetRevision, "spec.config[].gitRef.targetRevision", 0, 1024, nil, "")...)
	}

	containsParams, paramErrs = validateParametersInString(&c.GitRef.Path, "spec.config[].gitRef.path", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateString(&c.GitRef.Path, "spec.config[].gitRef.path", 0, 2048, nil, "")...)
	}
	return allErrs
}

func (c KubernetesSecretProviderSpec) Validate(fleetTemplate bool) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)

	containsParams, paramErrs := validateParametersInString(&c.SecretRef.Name, "spec.config[].secretRef.name", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Name, "spec.config[].secretRef.name")...)
	}

	containsParams, paramErrs = validateParametersInString(&c.SecretRef.Namespace, "spec.config[].secretRef.namespace", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateGenericName(&c.SecretRef.Namespace, "spec.config[].secretRef.namespace")...)
	}

	containsParams, paramErrs = validateParametersInString(&c.SecretRef.MountPath, "spec.config[].secretRef.mountPath", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateFilePath(&c.SecretRef.MountPath, "spec.config[].secretRef.mountPath")...)
		if err := validation.DenyForbiddenDevicePath(c.SecretRef.MountPath); err != nil {
			allErrs = append(allErrs, fmt.Errorf("spec.config[].secretRef.mountPath: %w", err))
		}
	}

	return allErrs
}

func (c InlineConfigProviderSpec) Validate(fleetTemplate bool) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&c.Name, "spec.config[].name")...)
	for i := range c.Inline {
		containsParams, paramErrs := validateParametersInString(&c.Inline[i].Path, fmt.Sprintf("spec.config[].inline[%d].path", i), fleetTemplate)
		allErrs = append(allErrs, paramErrs...)
		if !containsParams {
			allErrs = append(allErrs, validation.ValidateFilePath(&c.Inline[i].Path, fmt.Sprintf("spec.config[].inline[%d].path", i))...)
			if err := validation.DenyForbiddenDevicePath(c.Inline[i].Path); err != nil {
				allErrs = append(allErrs, fmt.Errorf("spec.config[].inline[%d].path: %w", i, err))
			}
		}

		allErrs = append(allErrs, validation.ValidateLinuxUserGroup(c.Inline[i].User, fmt.Sprintf("spec.config[].inline[%d].user", i))...)
		allErrs = append(allErrs, validation.ValidateLinuxUserGroup(c.Inline[i].Group, fmt.Sprintf("spec.config[].inline[%d].group", i))...)
		allErrs = append(allErrs, validation.ValidateLinuxFileMode(c.Inline[i].Mode, fmt.Sprintf("spec.config[].inline[%d].mode", i))...)

		if c.Inline[i].ContentEncoding != nil && *(c.Inline[i].ContentEncoding) == EncodingBase64 {
			// Contents should be base64 encoded and limited to 1MB (1024*1024=1048576 bytes)
			allErrs = append(allErrs, validation.ValidateBase64Field(c.Inline[i].Content, fmt.Sprintf("spec.config[].inline[%d].content", i), maxInlineLength)...)
			// Can ignore errors because we just validated it in the previous line
			b, _ := base64.StdEncoding.DecodeString(c.Inline[i].Content)
			_, paramErrs = validateParametersInString(lo.ToPtr(string(b)), "spec.config[].inline[%d].content", fleetTemplate)
			allErrs = append(allErrs, paramErrs...)
		} else if c.Inline[i].ContentEncoding == nil || (c.Inline[i].ContentEncoding != nil && *(c.Inline[i].ContentEncoding) == EncodingPlain) {
			// Contents should be limited to 1MB (1024*1024=1048576 bytes)
			allErrs = append(allErrs, validation.ValidateString(&c.Inline[i].Content, fmt.Sprintf("spec.config[].inline[%d].content", i), 0, maxInlineLength, nil, "")...)
			_, paramErrs = validateParametersInString(&c.Inline[i].Content, fmt.Sprintf("spec.config[].inline[%d].content", i), fleetTemplate)
			allErrs = append(allErrs, paramErrs...)
		} else {
			allErrs = append(allErrs, fmt.Errorf("unknown contentEncoding: %s", *(c.Inline[i].ContentEncoding)))
		}
	}
	return allErrs
}

func (h HttpConfigProviderSpec) Validate(fleetTemplate bool) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateGenericName(&h.Name, "spec.config[].name")...)
	allErrs = append(allErrs, validation.ValidateResourceNameReference(&h.HttpRef.Repository, "spec.config[].httpRef.repository")...)

	containsParams, paramErrs := validateParametersInString(&h.HttpRef.FilePath, "spec.config[].httpRef.filePath", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateFilePath(&h.HttpRef.FilePath, "spec.config[].httpRef.filePath")...)
		if err := validation.DenyForbiddenDevicePath(h.HttpRef.FilePath); err != nil {
			allErrs = append(allErrs, fmt.Errorf("spec.config[].httpRef.filePath: %w", err))
		}
	}

	containsParams, paramErrs = validateParametersInString(h.HttpRef.Suffix, "spec.config[].httpRef.suffix", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateString(h.HttpRef.Suffix, "spec.config[].httpRef.suffix", 0, 2048, nil, "")...)
	}

	return allErrs
}

func (r EnrollmentRequest) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, validation.ValidateCSRWithTCGSupport([]byte(r.Spec.Csr))...)

	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged for EnrollmentRequest.
func (er *EnrollmentRequest) ValidateUpdate(newObj *EnrollmentRequest) []error {
	return validateImmutableCoreFields(er.Metadata.Name, newObj.Metadata.Name,
		er.ApiVersion, newObj.ApiVersion,
		er.Kind, newObj.Kind,
		er.Status, newObj.Status)
}

func (r EnrollmentRequestApproval) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateLabelsWithPath(r.Labels, "labels")...)
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
	allErrs = append(allErrs, validation.ValidateCSRWithTCGSupport(r.Spec.Request)...)
	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged for CertificateSigningRequest.
func (csr *CertificateSigningRequest) ValidateUpdate(newObj *CertificateSigningRequest) []error {
	return validateImmutableCoreFields(csr.Metadata.Name, newObj.Metadata.Name,
		csr.ApiVersion, newObj.ApiVersion,
		csr.Kind, newObj.Kind,
		csr.Status, newObj.Status)
}

func (b *Batch_Limit) Validate() []error {
	if b == nil {
		return nil
	}
	intVal, err := b.AsBatchLimit1()
	if err == nil {
		if intVal <= 0 {
			return []error{errors.New("absolute limit value must be positive integer")}
		}
		return nil
	}
	p, err := b.AsPercentage()
	if err != nil {
		return []error{fmt.Errorf("limit must either an integer value or a percentage: %w", err)}
	}
	if err = validatePercentage(p); err != nil {
		return []error{err}
	}
	return nil
}

func (b *Batch) Validate() []error {
	var errs []error
	if b == nil {
		return []error{errors.New("a batch in a batch sequence must not be null")}
	}
	errs = append(errs, b.Selector.Validate()...)
	errs = append(errs, b.Limit.Validate()...)
	if b.SuccessThreshold != nil {
		if err := validatePercentage(*b.SuccessThreshold); err != nil {
			errs = append(errs, fmt.Errorf("batch success threshold: %w", err))
		}
	}
	return errs
}

func (b BatchSequence) Validate() []error {
	var errs []error
	for _, batch := range lo.FromPtr(b.Sequence) {
		errs = append(errs, batch.Validate()...)
	}
	return errs
}

func (r *RolloutDeviceSelection) Validate() []error {
	var errs []error
	if r == nil {
		return nil
	}
	i, err := r.ValueByDiscriminator()
	if err != nil {
		errs = append(errs, err)
	} else {
		switch v := i.(type) {
		case BatchSequence:
			errs = append(errs, v.Validate()...)
		}
	}
	return errs
}

func (d *DisruptionBudget) Validate() []error {
	var errs []error
	if d == nil {
		return nil
	}
	if d.MinAvailable == nil && d.MaxUnavailable == nil {
		errs = append(errs, errors.New("at least one of [MinAvailable, MaxUnavailable] must be defined in disruption budget"))
	}
	groupBy := lo.FromPtr(d.GroupBy)
	if len(groupBy) != len(lo.Uniq(groupBy)) {
		errs = append(errs, errors.New("groupBy items must be unique"))
	}
	return errs
}

func (r *RolloutPolicy) Validate() []error {
	var errs []error
	if r == nil {
		return nil
	}
	if r.DeviceSelection == nil && r.DisruptionBudget == nil {
		errs = append(errs, errors.New("at least one of [DeviceSelection, DisruptionBudget] must be defined"))
	}
	errs = append(errs, r.DeviceSelection.Validate()...)
	errs = append(errs, r.DisruptionBudget.Validate()...)
	if r.SuccessThreshold != nil {
		if err := validatePercentage(*r.SuccessThreshold); err != nil {
			errs = append(errs, fmt.Errorf("rollout policy success threshold: %w", err))
		}
	}
	return errs
}

func (r Fleet) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(r.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(r.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(r.Metadata.Annotations)...)
	allErrs = append(allErrs, r.Spec.Selector.Validate()...)
	allErrs = append(allErrs, r.Spec.RolloutPolicy.Validate()...)

	// Validate the Device spec settings
	allErrs = append(allErrs, r.Spec.Template.Spec.Validate(true)...)

	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged for Fleet.
func (f *Fleet) ValidateUpdate(newObj *Fleet) []error {
	return validateImmutableCoreFields(f.Metadata.Name, newObj.Metadata.Name,
		f.ApiVersion, newObj.ApiVersion,
		f.Kind, newObj.Kind,
		f.Status, newObj.Status)
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
		if err := validateTimeZone(lo.FromPtr(u.TimeZone)); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	// allow only the standard 5 input cron syntax e.g. "* * * * *"
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(u.At)
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid cron schedule: %s", err))
	}

	if err := validateGraceDuration(schedule, u.StartGraceDuration); err != nil {
		allErrs = append(allErrs, err)
	}

	return allErrs
}

func (r *Repository) Validate() []error {
	if r == nil {
		return nil
	}
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

// ValidateUpdate ensures immutable fields are unchanged for Repository.
func (r *Repository) ValidateUpdate(newObj *Repository) []error {
	return validateImmutableCoreFields(r.Metadata.Name, newObj.Metadata.Name,
		r.ApiVersion, newObj.ApiVersion,
		r.Kind, newObj.Kind,
		r.Status, newObj.Status)
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

// ValidateUpdate ensures immutable fields are unchanged for ResourceSync.
func (rs *ResourceSync) ValidateUpdate(newObj *ResourceSync) []error {
	return validateImmutableCoreFields(rs.Metadata.Name, newObj.Metadata.Name,
		rs.ApiVersion, newObj.ApiVersion,
		rs.Kind, newObj.Kind,
		rs.Status, newObj.Status)
}

func (tv TemplateVersion) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(tv.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateResourceOwner(tv.Metadata.Owner, lo.ToPtr(FleetKind))...)
	_, passedOwnerResource, _ := util.GetResourceOwner(tv.Metadata.Owner)
	if passedOwnerResource != tv.Spec.Fleet {
		allErrs = append(allErrs, errors.New("metadata.owner and spec.fleet must match"))
	}
	return allErrs
}

func (l *LabelSelector) Validate() []error {
	if l != nil && l.MatchExpressions == nil && l.MatchLabels == nil {
		return []error{errors.New("at least one of [matchLabels,matchExpressions] must appear in a label selector")}
	}
	return nil
}

func (d *DeviceSystemInfo) IsEmpty() bool {
	empty := DeviceSystemInfo{}
	return reflect.DeepEqual(*d, empty)
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

func (a ApplicationProviderSpec) Validate() []error {
	allErrs := []error{}
	// name must be 1–253 characters long, start with a letter or number, and contain no whitespace
	allErrs = append(allErrs, validation.ValidateString(a.Name, "spec.applications[].name", 1, validation.DNS1123MaxLength, validation.GenericNameRegexp, validation.Dns1123LabelFmt)...)
	// envVars keys and values must be between 1 and 253 characters
	allErrs = append(allErrs, validation.ValidateStringMap(a.EnvVars, "spec.applications[].envVars", 1, validation.DNS1123MaxLength, validation.EnvVarNameRegexp, nil, "")...)
	return allErrs
}

func (a InlineApplicationProviderSpec) Validate(appType AppType, fleetTemplate bool) []error {
	allErrs := []error{}

	var appValidator applicationValidator
	switch appType {
	case AppTypeCompose:
		appValidator = &composeValidator{paths: make(map[string]struct{})}
	case AppTypeQuadlet:
		appValidator = &quadletValidator{quadlets: make(map[string]*common.QuadletReferences)}
	default:
		appValidator = &unknownAppTypeValidator{appType}
	}

	seenPath := make(map[string]struct{}, len(a.Inline))
	for i := range a.Inline {
		path := a.Inline[i].Path
		// ensure uniqueness of path per application
		if _, exists := seenPath[path]; exists {
			allErrs = append(allErrs, fmt.Errorf("duplicate inline path: %s", path))
		} else {
			seenPath[path] = struct{}{}
		}
		allErrs = append(allErrs, a.Inline[i].Validate(i, appValidator, fleetTemplate)...)
	}

	allErrs = append(allErrs, appValidator.Validate()...)
	return allErrs
}

func (c ApplicationContent) Validate(index int, appValidator applicationValidator, fleetTemplate bool) []error {
	var allErrs []error
	pathPrefix := fmt.Sprintf("spec.applications[].inline[%d]", index)

	// validate path
	containsParams, paramErrs := validateParametersInString(&c.Path, pathPrefix+".path", fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	if !containsParams {
		allErrs = append(allErrs, validation.ValidateRelativePath(&c.Path, pathPrefix+".path", 253)...)
	}

	content := lo.FromPtr(c.Content)
	if content == "" {
		return allErrs
	}

	var decodedBytes []byte
	var decodedStr string
	contentPath := fmt.Sprintf("%s.content", pathPrefix)
	if c.IsBase64() {
		var err error
		decodedBytes, err = base64.StdEncoding.DecodeString(content)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("decode base64 content: %w", err))
			return allErrs
		}
		decodedStr = string(decodedBytes)
	} else if c.IsPlain() {
		decodedStr = content
		decodedBytes = []byte(content)
	} else {
		allErrs = append(allErrs, fmt.Errorf("unknown encoding type: %s", *c.ContentEncoding))
		return allErrs
	}

	// validate content
	allErrs = append(allErrs, validation.ValidateString(&decodedStr, contentPath, 0, maxInlineLength, nil, "")...)
	_, paramErrs = validateParametersInString(&decodedStr, contentPath, fleetTemplate)
	allErrs = append(allErrs, paramErrs...)
	allErrs = append(allErrs, appValidator.ValidateContents(c.Path, decodedBytes, fleetTemplate)...)

	return allErrs
}

func (c ApplicationContent) IsBase64() bool {
	return c.ContentEncoding != nil && *c.ContentEncoding == EncodingBase64
}

func (c ApplicationContent) IsPlain() bool {
	return c.ContentEncoding == nil || *c.ContentEncoding == EncodingPlain
}

func validateApplications(apps []ApplicationProviderSpec, fleetTemplate bool) []error {
	allErrs := []error{}
	seenAppNames := make(map[string]struct{})

	for _, app := range apps {
		seenVolumeNames := make(map[string]struct{})
		providerType, err := validateAppProviderType(app)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}

		appName, err := ensureAppName(app, providerType)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}

		if _, exists := seenAppNames[appName]; exists {
			allErrs = append(allErrs, fmt.Errorf("duplicate application name: %s", appName))
			continue
		}
		seenAppNames[appName] = struct{}{}

		if app.AppType == "" {
			allErrs = append(allErrs, fmt.Errorf("app type must be defined for application: %s", appName))
			continue
		}

		var volumes *[]ApplicationVolume
		switch providerType {
		case ImageApplicationProviderType:
			provider, err := app.AsImageApplicationProviderSpec()
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("invalid image application provider: %w", err))
				continue
			}
			if provider.Image == "" && app.Name == nil {
				allErrs = append(allErrs, fmt.Errorf("image reference cannot be empty when application name is not provided"))
			}
			allErrs = append(allErrs, validateOciImageReference(&provider.Image, fmt.Sprintf("spec.applications[%s].image", appName), fleetTemplate)...)
			if app.AppType != AppTypeContainer {
				if provider.Ports != nil && len(*provider.Ports) > 0 {
					allErrs = append(allErrs, fmt.Errorf("ports can only be defined for container applications, not %q", app.AppType))
				}
				if provider.Resources != nil {
					allErrs = append(allErrs, fmt.Errorf("resources can only be defined for container applications, not %q", app.AppType))
				}
			} else {
				allErrs = append(allErrs, ValidateContainerImageApplicationSpec(appName, &provider)...)
			}

			volumes = provider.Volumes

		case InlineApplicationProviderType:
			provider, err := app.AsInlineApplicationProviderSpec()
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("invalid inline application provider: %w", err))
				continue
			}
			if app.AppType == AppTypeContainer {
				allErrs = append(allErrs, fmt.Errorf("inline application type must not be %q", AppTypeContainer))
			}
			allErrs = append(allErrs, provider.Validate(app.AppType, fleetTemplate)...)
			volumes = provider.Volumes
		default:
			allErrs = append(allErrs, fmt.Errorf("no validations implemented for application provider type: %s", providerType))
			continue
		}

		allErrs = append(allErrs, app.Validate()...)

		if volumes != nil {
			for i, vol := range *volumes {
				path := fmt.Sprintf("spec.applications[%s].volumes[%d]", appName, i)
				if _, exists := seenVolumeNames[vol.Name]; exists {
					allErrs = append(allErrs, fmt.Errorf("duplicate volume name for application: %s", vol.Name))
					continue
				}
				seenVolumeNames[vol.Name] = struct{}{}

				allErrs = append(allErrs, validation.ValidateString(&vol.Name, path+".name", 1, 253, validation.GenericNameRegexp, "")...)
				allErrs = append(allErrs, validateVolume(vol, path, fleetTemplate, app.AppType)...)
			}
		}
	}

	return allErrs
}

func ValidateContainerImageApplicationSpec(appName string, spec *ImageApplicationProviderSpec) []error {
	errs := validateContainerPorts(spec.Ports, fmt.Sprintf("spec.applications[%s].ports", appName))
	if spec.Resources != nil && spec.Resources.Limits != nil {
		errs = append(errs, validatePodmanCPULimit(spec.Resources.Limits.Cpu, fmt.Sprintf("spec.applications[%s].resources.limits.cpu", appName))...)
		errs = append(errs, validatePodmanMemoryLimit(spec.Resources.Limits.Memory, fmt.Sprintf("spec.applications[%s].resources.limits.memory", appName))...)
	}
	return errs
}

func validateVolume(vol ApplicationVolume, path string, fleetTemplate bool, appType AppType) []error {
	var errs []error

	if vol.ReclaimPolicy != nil && *vol.ReclaimPolicy != Retain {
		errs = append(errs, fmt.Errorf("%s.reclaimPolicy: only %q is supported", path, Retain))
	}

	providerType, err := vol.Type()
	if err != nil {
		return []error{fmt.Errorf("invalid application volume provider: %w", err)}
	}

	switch providerType {
	case ImageApplicationVolumeProviderType:
		imgProvider, err := vol.AsImageVolumeProviderSpec()
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid image application volume provider: %w", err))
		} else {
			errs = append(errs, validateOciImageReference(&imgProvider.Image.Reference, path+".image.reference", fleetTemplate)...)
		}
		if appType == AppTypeContainer {
			errs = append(errs, fmt.Errorf("image application volume provider invalid for app type: %s", appType))
		}
	case MountApplicationVolumeProviderType:
		mountProvider, err := vol.AsMountVolumeProviderSpec()
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid mount application volume provider: %w", err))
		} else {
			pathParts := strings.Split(mountProvider.Mount.Path, ":")
			errs = append(errs, validation.ValidateFilePath(&pathParts[0], path+".mount.path")...)
		}
		if appType != AppTypeContainer {
			errs = append(errs, fmt.Errorf("mount application volume provider invalid for app type: %s", appType))
		}
	case ImageMountApplicationVolumeProviderType:
		provider, err := vol.AsImageMountVolumeProviderSpec()
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid image mount application volume provider: %w", err))
		} else {
			pathParts := strings.Split(provider.Mount.Path, ":")
			errs = append(errs, validation.ValidateFilePath(&pathParts[0], path+".mount.path")...)
			errs = append(errs, validateOciImageReference(&provider.Image.Reference, path+".image.reference", fleetTemplate)...)
		}
		if appType != AppTypeContainer {
			errs = append(errs, fmt.Errorf("image mount application volume provider invalid for app type: %s", appType))
		}

	default:
		errs = append(errs, fmt.Errorf("unknown application volume provider type: %s", providerType))
	}

	return errs
}

func validateContainerPorts(ports *[]ApplicationPort, path string) []error {
	if ports == nil || len(*ports) == 0 {
		return nil
	}

	var allErrs []error
	portPattern := regexp.MustCompile(`^[0-9]+:[0-9]+$`)

	for i, portString := range *ports {
		formatErr := fmt.Errorf("%s[%d]: must be in format 'portnumber:portnumber', got %q", path, i, portString)
		if !portPattern.MatchString(portString) {
			allErrs = append(allErrs, formatErr)
			continue
		}
		portParts := strings.Split(portString, ":")
		if len(portParts) != 2 {
			allErrs = append(allErrs, formatErr)
			continue
		}

		for _, port := range portParts {
			numberErr := fmt.Errorf("%s[%d]: must be a number in the valid port range of [1, 65535], got: %q", path, i, port)
			portNumber, err := strconv.Atoi(port)
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("%w: %w", numberErr, err))
				continue
			}
			if portNumber < privilegedPortRangeStart || portNumber > portRangeEnd {
				allErrs = append(allErrs, numberErr)
			}
		}
	}
	return allErrs
}

func validatePodmanCPULimit(cpu *string, path string) []error {
	var errs []error
	if cpu == nil {
		return errs
	}

	val, err := strconv.ParseFloat(*cpu, 64)
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: must be a valid number, got %q", path, *cpu))
	} else if val < 0 {
		errs = append(errs, fmt.Errorf("%s: must be positive. got %q", path, *cpu))
	}
	return errs
}

var podmanMemoryLimitPattern = regexp.MustCompile(`^[0-9]+[bkmg]?$`)

func validatePodmanMemoryLimit(memory *string, path string) []error {
	if memory == nil {
		return nil
	}

	if !podmanMemoryLimitPattern.MatchString(*memory) {
		return []error{fmt.Errorf("%s: must be in format 'number[unit]' where unit is b, k, m, or g, got %q", path, *memory)}
	}
	return nil
}

func validateAppProviderType(app ApplicationProviderSpec) (ApplicationProviderType, error) {
	providerType, err := app.Type()
	if err != nil {
		return "", fmt.Errorf("application type error: %w", err)
	}

	switch providerType {
	case ImageApplicationProviderType:
		return providerType, nil
	case InlineApplicationProviderType:
		return providerType, nil
	default:
		return "", fmt.Errorf("unknown application provider type: %s", providerType)
	}
}

func ensureAppName(app ApplicationProviderSpec, appType ApplicationProviderType) (string, error) {
	switch appType {
	case ImageApplicationProviderType:
		provider, err := app.AsImageApplicationProviderSpec()
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
	case InlineApplicationProviderType:
		if app.Name == nil {
			return "", fmt.Errorf("inline application name cannot be empty")
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
	// validating the duration first so that we can potentially report more issues
	// to the caller if malformed duration is also applied
	graceDuration, err := time.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}

	if schedule == nil {
		return fmt.Errorf("invalid schedule: cannot validate grace duration")
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

// validateOciImageReference validates an OCI image reference, with template support if fleetTemplate is true
func validateOciImageReference(imageRef *string, path string, fleetTemplate bool) []error {
	containsParams, paramErrs := validateParametersInString(imageRef, path, fleetTemplate)
	allErrs := append([]error{}, paramErrs...)

	if !containsParams {
		allErrs = append(allErrs, validation.ValidateOciImageReference(imageRef, path)...)
	} else {
		allErrs = append(allErrs, validation.ValidateOciImageReferenceWithTemplates(imageRef, path)...)
	}

	return allErrs
}

func validateParametersInString(s *string, path string, fleetTemplate bool) (bool, []error) {
	// If we're not dealing with a fleet template, assume no parameters
	if s == nil || !fleetTemplate {
		return false, []error{}
	}

	allErrs := []error{}

	t, err := template.New("t").Option("missingkey=zero").Funcs(GetGoTemplateFuncMap()).Parse(*s)
	if err != nil {
		return false, validation.FormatInvalidError(*s, path, fmt.Sprintf("invalid parameter syntax: %v", err))
	}

	// Ensure template has only supported types
	for _, node := range t.Root.Nodes {
		allowedNodeTypes := []parse.NodeType{
			parse.NodeText,   // Plain text
			parse.NodeAction, // A non-control action such as a field evaluation
		}
		if !slices.Contains(allowedNodeTypes, node.Type()) {
			return false, validation.FormatInvalidError(*s, path, fmt.Sprintf("template contains unsupported elements: %s", node.String()))
		}
	}

	// When the template is executed here, any missing label/annotation keys are evaluated to empty
	// strings, so an empty map is fine.
	dev := &Device{
		Metadata: ObjectMeta{
			Name:   lo.ToPtr("name"),
			Labels: &map[string]string{},
		},
	}

	output, err := ExecuteGoTemplateOnDevice(t, dev)
	if err != nil {
		return false, validation.FormatInvalidError(*s, path, fmt.Sprintf("cannot apply parameters, possibly because they access invalid fields: %v", err))
	}
	return output != *s, allErrs
}

func ValidateConditions(conditions []Condition, allowedConditions, trueConditions, exclusiveConditions []ConditionType) []error {
	allErrs := []error{}
	seen := make(map[ConditionType]bool)
	seenExclusives := make(map[ConditionType]bool)
	for _, c := range conditions {
		if !slices.Contains(allowedConditions, c.Type) {
			allErrs = append(allErrs, fmt.Errorf("not allowed condition type %q", c.Type))
		}
		if slices.Contains(trueConditions, c.Type) && c.Status != ConditionStatusTrue {
			allErrs = append(allErrs, fmt.Errorf("condition %q may only be set to 'true'", c.Type))
		}
		if _, exists := seen[c.Type]; exists {
			allErrs = append(allErrs, fmt.Errorf("duplicate condition type %q", c.Type))
		}
		seen[c.Type] = true
		if slices.Contains(exclusiveConditions, c.Type) {
			seenExclusives[c.Type] = true
		}
	}
	if len(seenExclusives) > 1 {
		allErrs = append(allErrs, fmt.Errorf("only one of %v may be set", exclusiveConditions))
	}
	return allErrs
}

func validatePercentage(p Percentage) error {
	pattern := `^(100|[1-9]?[0-9])%$`
	matched, err := regexp.MatchString(pattern, p)
	if err != nil {
		return fmt.Errorf("failed to match percentage %s: %w", p, err)
	}
	if !matched {
		return fmt.Errorf("'%s' doesn't match percentage pattern '%s'", p, pattern)
	}
	return nil
}

// validateImmutableCoreFields is a helper used by ValidateUpdate to ensure name, apiVersion, kind and status are unchanged.
func validateImmutableCoreFields(currentName, newName *string, currentAPIVersion, newAPIVersion, currentKind, newKind string, currentStatus, newStatus interface{}) []error {
	var errs []error
	if newName == nil || *currentName != *newName {
		errs = append(errs, fmt.Errorf("metadata.name is immutable"))
	}
	if currentAPIVersion != newAPIVersion {
		errs = append(errs, fmt.Errorf("apiVersion is immutable"))
	}
	if currentKind != newKind {
		errs = append(errs, fmt.Errorf("kind is immutable"))
	}
	if !reflect.DeepEqual(currentStatus, newStatus) {
		errs = append(errs, fmt.Errorf("status is immutable"))
	}
	return errs
}

// validateResourceMonitor performs cross-resource validation for ResourceMonitor arrays
func validateResourceMonitor(resources []ResourceMonitor) []error {
	var allErrs []error

	// Validate no duplicate monitorTypes exist across resources
	// Each monitorType (CPU, Disk, Memory) should only appear once in the resources array
	seenMonitorTypes := make(map[string]struct{})
	for _, resource := range resources {
		monitorType, err := resource.Discriminator()
		if err == nil {
			if _, exists := seenMonitorTypes[monitorType]; exists {
				allErrs = append(allErrs, fmt.Errorf("%w: %s", ErrDuplicateMonitorType, monitorType))
			} else {
				seenMonitorTypes[monitorType] = struct{}{}
			}
		}
	}

	return allErrs
}

// hasPathField checks if the raw JSON contains a "path" field
func hasPathField(rawJSON []byte) bool {
	var data map[string]interface{}
	if err := json.Unmarshal(rawJSON, &data); err != nil {
		return false
	}
	_, exists := data["path"]
	return exists
}

func (a *AuthProvider) Validate(ctx context.Context) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(a.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(a.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(a.Metadata.Annotations)...)
	allErrs = append(allErrs, a.Spec.Validate(ctx, false)...)

	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged and required fields are not deleted for AuthProvider.
func (a *AuthProvider) ValidateUpdate(ctx context.Context, oldObj *AuthProvider) []error {
	allErrs := validateImmutableCoreFields(oldObj.Metadata.Name, a.Metadata.Name,
		oldObj.ApiVersion, a.ApiVersion,
		oldObj.Kind, a.Kind,
		nil, nil) // AuthProvider doesn't have status

	// Validate the new spec (receiver 'a' is the new object)
	allErrs = append(allErrs, a.Spec.ValidateUpdate(ctx, &oldObj.Spec)...)

	return allErrs
}

func (o *OIDCProviderSpec) Validate(ctx context.Context, isUpdate bool) []error {
	allErrs := []error{}

	if o.Issuer == "" {
		allErrs = append(allErrs, ErrIssuerRequired)
	}
	if o.ClientId == "" {
		allErrs = append(allErrs, ErrClientIdRequired)
	}

	// Validate organization assignment
	allErrs = append(allErrs, o.OrganizationAssignment.Validate(ctx)...)

	// Validate role assignment
	allErrs = append(allErrs, o.RoleAssignment.Validate(ctx)...)

	return allErrs
}

func (o *OAuth2ProviderSpec) Validate(ctx context.Context, isUpdate bool) []error {
	allErrs := []error{}

	if o.AuthorizationUrl == "" {
		allErrs = append(allErrs, ErrAuthorizationUrlRequired)
	}
	if o.TokenUrl == "" {
		allErrs = append(allErrs, ErrTokenUrlRequired)
	}
	if o.UserinfoUrl == "" {
		allErrs = append(allErrs, ErrUserinfoUrlRequired)
	}
	if o.ClientId == "" {
		allErrs = append(allErrs, ErrClientIdRequired)
	}

	// Validate introspection field is present
	if o.Introspection == nil {
		allErrs = append(allErrs, fmt.Errorf("introspection field is required for OAuth2 providers"))
	}

	// Validate organization assignment
	allErrs = append(allErrs, o.OrganizationAssignment.Validate(ctx)...)

	// Validate role assignment
	allErrs = append(allErrs, o.RoleAssignment.Validate(ctx)...)

	return allErrs
}

func (a *AuthProviderSpec) Validate(ctx context.Context, isUpdate bool) []error {
	allErrs := []error{}

	// Get the discriminator to determine which type of provider this is
	discriminator, err := a.Discriminator()
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid auth provider spec: %w", err))
		return allErrs
	}

	switch discriminator {
	case string(Oidc):
		oidcSpec, err := a.AsOIDCProviderSpec()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid OIDC provider spec: %w", err))
		} else {
			allErrs = append(allErrs, (&oidcSpec).Validate(ctx, isUpdate)...)
		}
	case string(Oauth2):
		oauth2Spec, err := a.AsOAuth2ProviderSpec()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid OAuth2 provider spec: %w", err))
		} else {
			allErrs = append(allErrs, (&oauth2Spec).Validate(ctx, isUpdate)...)
		}
	case string(K8s):
		allErrs = append(allErrs, ErrK8sProviderConfigOnly)
	case string(Aap):
		allErrs = append(allErrs, ErrAapProviderConfigOnly)
	default:
		allErrs = append(allErrs, fmt.Errorf("unknown provider type: %s", discriminator))
	}

	return allErrs
}

// ValidateUpdate ensures required fields are not deleted for AuthProviderSpec.
func (a *AuthProviderSpec) ValidateUpdate(ctx context.Context, oldSpec *AuthProviderSpec) []error {
	allErrs := []error{}

	// Check both old and new provider types for OAuth2 to validate introspection field
	oldDiscriminator, oldErr := oldSpec.Discriminator()
	if oldErr != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid old auth provider spec: %w", oldErr))
		return allErrs
	}

	newDiscriminator, newErr := a.Discriminator()
	if newErr != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid new auth provider spec: %w", newErr))
		return allErrs
	}

	// If the old provider was OAuth2, check for introspection removal
	if oldDiscriminator == string(Oauth2) {
		oldOAuth2Spec, err := oldSpec.AsOAuth2ProviderSpec()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("failed to parse old OAuth2 provider spec: %w", err))
			return allErrs
		}

		// Check if new provider is also OAuth2
		if newDiscriminator == string(Oauth2) {
			newOAuth2Spec, err := a.AsOAuth2ProviderSpec()
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("invalid new OAuth2 provider spec: %w", err))
				return allErrs
			}

			// Check if introspection is being removed
			if oldOAuth2Spec.Introspection != nil && newOAuth2Spec.Introspection == nil {
				allErrs = append(allErrs, fmt.Errorf("introspection field cannot be removed once set"))
			}
		}
	}

	// Run standard validation with isUpdate=true
	allErrs = append(allErrs, a.Validate(ctx, true)...)

	return allErrs
}

func (a AuthOrganizationAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	// Get the discriminator to determine which type of assignment this is
	discriminator, err := a.Discriminator()
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid organization assignment: %w", err))
		return allErrs
	}

	switch discriminator {
	case string(AuthStaticOrganizationAssignmentTypeStatic):
		static, err := a.AsAuthStaticOrganizationAssignment()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid static organization assignment: %w", err))
		} else {
			allErrs = append(allErrs, static.Validate(ctx)...)
		}
	case string(AuthDynamicOrganizationAssignmentTypeDynamic):
		dynamic, err := a.AsAuthDynamicOrganizationAssignment()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid dynamic organization assignment: %w", err))
		} else {
			allErrs = append(allErrs, dynamic.Validate(ctx)...)
		}
	case string(PerUser):
		perUser, err := a.AsAuthPerUserOrganizationAssignment()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid per-user organization assignment: %w", err))
		} else {
			allErrs = append(allErrs, perUser.Validate(ctx)...)
		}
	default:
		allErrs = append(allErrs, fmt.Errorf("unknown organization assignment type: %s", discriminator))
	}

	return allErrs
}

func (a AuthStaticOrganizationAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	if a.OrganizationName == "" {
		allErrs = append(allErrs, ErrOrganizationNameRequired)
	}

	// For non-admin users, validate that the static organization matches their current organization
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		allErrs = append(allErrs, ErrMappedIdentityNotFound)
		return allErrs
	}

	// Check if user is super admin
	hasAdminRole := mappedIdentity.IsSuperAdmin()

	// If user is not admin, they can only assign to their current organization
	if !hasAdminRole {
		organizations := mappedIdentity.GetOrganizations()
		// Check if the organization name matches any of the user's organizations
		hasMatchingOrg := false
		for _, org := range organizations {
			if org.ExternalID == a.OrganizationName {
				hasMatchingOrg = true
				break
			}
		}
		if !hasMatchingOrg {
			// Build a list of valid organization names for error message
			validOrgs := make([]string, len(organizations))
			for i, org := range organizations {
				validOrgs[i] = org.ExternalID
			}
			allErrs = append(allErrs, fmt.Errorf("non-admin users can only assign to one of their current organizations: %v", validOrgs))
		}
	}

	return allErrs
}

func (a AuthDynamicOrganizationAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	if len(a.ClaimPath) == 0 {
		allErrs = append(allErrs, ErrClaimPathRequiredDynamicOrg)
	}

	// Only flightctl-admin is allowed to create auth providers with dynamic org mapping
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		allErrs = append(allErrs, ErrMappedIdentityNotFound)
		return allErrs
	}

	// Only super admin users can create dynamic organization mappings
	if !mappedIdentity.IsSuperAdmin() {
		allErrs = append(allErrs, ErrDynamicOrgMappingAdminOnly)
	}

	return allErrs
}

func (a AuthPerUserOrganizationAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	// Per-user assignment doesn't require additional validation
	// The organization name will be generated from the user's identity

	// Only flightctl-admin is allowed to create auth providers with per-user org mapping
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		allErrs = append(allErrs, ErrMappedIdentityNotFound)
		return allErrs
	}

	// Only super admin users can create per-user organization mappings
	if !mappedIdentity.IsSuperAdmin() {
		allErrs = append(allErrs, ErrPerUserOrgMappingAdminOnly)
	}

	return allErrs
}

func (a AuthRoleAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	// Get the discriminator to determine which type of assignment this is
	discriminator, err := a.Discriminator()
	if err != nil {
		allErrs = append(allErrs, fmt.Errorf("invalid role assignment: %w", err))
		return allErrs
	}

	switch discriminator {
	case string(AuthStaticRoleAssignmentTypeStatic):
		static, err := a.AsAuthStaticRoleAssignment()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid static role assignment: %w", err))
		} else {
			allErrs = append(allErrs, static.Validate(ctx)...)
		}
	case string(AuthDynamicRoleAssignmentTypeDynamic):
		dynamic, err := a.AsAuthDynamicRoleAssignment()
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("invalid dynamic role assignment: %w", err))
		} else {
			allErrs = append(allErrs, dynamic.Validate(ctx)...)
		}
	default:
		allErrs = append(allErrs, fmt.Errorf("unknown role assignment type: %s", discriminator))
	}

	return allErrs
}

func (a AuthStaticRoleAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	if len(a.Roles) == 0 {
		allErrs = append(allErrs, ErrRolesRequired)
	}

	// Validate that all roles are non-empty strings
	for i, role := range a.Roles {
		if role == "" {
			allErrs = append(allErrs, fmt.Errorf("role at index %d cannot be empty", i))
		}
		if !slices.Contains(KnownExternalRoles, role) {
			allErrs = append(allErrs, fmt.Errorf("role at index %d is not a valid role: %s", i, role))
		}
	}

	// Check if any role is flightctl-admin - only super admins can create static mappings for this role
	hasAdminRole := false
	for _, role := range a.Roles {
		if role == ExternalRoleAdmin {
			hasAdminRole = true
			break
		}
	}

	if hasAdminRole {
		mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
		if !ok {
			allErrs = append(allErrs, ErrMappedIdentityNotFound)
			return allErrs
		}

		// Only super admin users can create static role mappings for flightctl-admin
		if !mappedIdentity.IsSuperAdmin() {
			allErrs = append(allErrs, ErrStaticRoleMappingAdminOnly)
		}
	}

	return allErrs
}

func (a AuthDynamicRoleAssignment) Validate(ctx context.Context) []error {
	allErrs := []error{}

	if len(a.ClaimPath) == 0 {
		allErrs = append(allErrs, ErrClaimPathRequiredDynamicRole)
	}

	return allErrs
}

func (s SystemdActiveStateType) Validate() error {
	validStates := []SystemdActiveStateType{
		SystemdActiveStateActivating,
		SystemdActiveStateActive,
		SystemdActiveStateDeactivating,
		SystemdActiveStateFailed,
		SystemdActiveStateInactive,
		SystemdActiveStateMaintenance,
		SystemdActiveStateRefreshing,
		SystemdActiveStateReloading,
		SystemdActiveStateUnknown,
	}
	if !slices.Contains(validStates, s) {
		return fmt.Errorf("invalid systemd active state: %s", s)
	}
	return nil
}

func (s SystemdEnableStateType) Validate() error {
	validStates := []SystemdEnableStateType{
		SystemdEnableStateAlias,
		SystemdEnableStateBad,
		SystemdEnableStateDisabled,
		SystemdEnableStateEmpty,
		SystemdEnableStateEnabled,
		SystemdEnableStateEnabledRuntime,
		SystemdEnableStateGenerated,
		SystemdEnableStateIndirect,
		SystemdEnableStateLinked,
		SystemdEnableStateLinkedRuntime,
		SystemdEnableStateMasked,
		SystemdEnableStateMaskedRuntime,
		SystemdEnableStateStatic,
		SystemdEnableStateTransient,
		SystemdEnableStateUnknown,
	}
	if !slices.Contains(validStates, s) {
		return fmt.Errorf("invalid systemd enable state: %s", s)
	}
	return nil
}

func (s SystemdLoadStateType) Validate() error {
	validStates := []SystemdLoadStateType{
		SystemdLoadStateBadSetting,
		SystemdLoadStateError,
		SystemdLoadStateLoaded,
		SystemdLoadStateMasked,
		SystemdLoadStateMerged,
		SystemdLoadStateNotFound,
		SystemdLoadStateStub,
		SystemdLoadStateUnknown,
	}
	if !slices.Contains(validStates, s) {
		return fmt.Errorf("invalid systemd load state: %s", s)
	}
	return nil
}

// InferOAuth2IntrospectionConfig attempts to infer a sensible introspection configuration
// based on the OAuth2 provider URLs. Returns an error if no introspection can be inferred.
func InferOAuth2IntrospectionConfig(spec OAuth2ProviderSpec) (*OAuth2Introspection, error) {
	// Check if this is a GitHub OAuth2 provider
	if strings.Contains(strings.ToLower(spec.AuthorizationUrl), "github") ||
		strings.Contains(strings.ToLower(spec.TokenUrl), "github") {
		introspection := &OAuth2Introspection{}
		githubSpec := GitHubIntrospectionSpec{
			Type: Github,
		}
		// Set URL if it's GitHub Enterprise (not github.com)
		if !strings.Contains(strings.ToLower(spec.AuthorizationUrl), "github.com") {
			// Extract base URL from authorization URL for GitHub Enterprise
			// e.g., https://github.enterprise.com/login/oauth/authorize -> https://api.github.enterprise.com
			baseURL := extractGitHubEnterpriseBaseURL(spec.AuthorizationUrl)
			if baseURL != "" {
				githubSpec.Url = &baseURL
			}
		}
		_ = introspection.FromGitHubIntrospectionSpec(githubSpec)
		return introspection, nil
	}

	// Try to infer RFC 7662 introspection endpoint
	// Common patterns: {tokenUrl}/introspect, {issuer}/introspect
	introspectionURL := inferRFC7662IntrospectionURL(spec)
	if introspectionURL != "" {
		introspection := &OAuth2Introspection{}
		rfc7662Spec := Rfc7662IntrospectionSpec{
			Type: Rfc7662,
			Url:  introspectionURL,
		}
		_ = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
		return introspection, nil
	}

	// No introspection could be inferred - reject
	return nil, fmt.Errorf("could not infer introspection configuration from provided URLs (authorizationUrl: %s, tokenUrl: %s); please specify introspection field explicitly", spec.AuthorizationUrl, spec.TokenUrl)
}

// extractGitHubEnterpriseBaseURL extracts the API base URL for GitHub Enterprise
// from an authorization URL like https://github.enterprise.com/login/oauth/authorize
func extractGitHubEnterpriseBaseURL(authURL string) string {
	// Try to parse the URL to extract the host
	if idx := strings.Index(authURL, "://"); idx != -1 {
		rest := authURL[idx+3:]
		if endIdx := strings.Index(rest, "/"); endIdx != -1 {
			host := rest[:endIdx]
			// For GitHub Enterprise, the API is typically at {host}/api/v3
			scheme := authURL[:idx]
			return scheme + "://" + host + "/api/v3"
		}
	}
	return ""
}

// inferRFC7662IntrospectionURL attempts to infer the RFC 7662 introspection endpoint URL
// based on common OAuth2 provider patterns
func inferRFC7662IntrospectionURL(spec OAuth2ProviderSpec) string {
	// Pattern 1: {tokenUrl}/introspect (most common)
	if spec.TokenUrl != "" {
		if introspectURL := buildIntrospectionURL(spec.TokenUrl); introspectURL != "" {
			return introspectURL
		}
	}

	// Pattern 2: {issuer}/introspect
	if spec.Issuer != nil && *spec.Issuer != "" {
		if introspectURL := buildIntrospectionURL(*spec.Issuer); introspectURL != "" {
			return introspectURL
		}
	}

	return ""
}

// buildIntrospectionURL constructs an introspection URL from a base URL,
// properly handling query parameters and URL components
func buildIntrospectionURL(baseURL string) string {
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return ""
	}

	// Remove trailing slash from path
	path := strings.TrimSuffix(parsedURL.Path, "/")

	// Check if path ends with /token and replace with /introspect
	if strings.HasSuffix(path, "/token") {
		path = strings.TrimSuffix(path, "/token") + "/introspect"
	} else {
		// Otherwise append /introspect
		path = path + "/introspect"
	}

	// Update the path in the parsed URL
	parsedURL.Path = path

	// Return the reassembled URL with all components preserved
	return parsedURL.String()
}

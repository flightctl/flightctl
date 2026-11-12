package v1beta1

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// FirmwareErrorReasonResourceLimitExceeded is the reason of a firmware error
// that reports an exceeded resource budget.
const FirmwareErrorReasonResourceLimitExceeded = "ResourceLimitExceeded"

const (
	firmwareStandaloneTemplateDetail = "template parameters are only supported in fleet templates"
	firmwareFixedFieldTemplateDetail = "template parameters are not supported in this field"
	firmwareConfigKeyTemplateDetail  = "template parameters are not supported in configuration keys"
	firmwareNameDetail               = "must be a lowercase RFC 1123 label of at most 63 characters: lowercase alphanumeric characters or '-', starting and ending with an alphanumeric character"
)

const (
	firmwareConfigMaxDepth  = 64
	firmwareConfigMaxNodes  = 10000
	firmwareConfigMaxErrors = 100
)

var (
	firmwareComponentNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	firmwareDigestPattern        = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

	firmwareAttemptPhases = []FirmwareAttemptPhase{
		FirmwareAttemptPhasePreparing,
		FirmwareAttemptPhaseWaitingForPolicy,
		FirmwareAttemptPhaseStaging,
		FirmwareAttemptPhaseAwaitingReboot,
		FirmwareAttemptPhaseVerifying,
		FirmwareAttemptPhaseSucceeded,
		FirmwareAttemptPhaseFailed,
		FirmwareAttemptPhaseCanceled,
	}
	firmwareCancellationReasons = []FirmwareCancellationReason{FirmwareCancellationReasonSuperseded}
	firmwareComponentTypes      = []FirmwareComponentType{FirmwareComponentTypeFirmware, FirmwareComponentTypeOverlay}
	firmwareComponentStates     = []FirmwareComponentState{
		FirmwareComponentStateUnknown,
		FirmwareComponentStatePending,
		FirmwareComponentStateActive,
		FirmwareComponentStateFailed,
	}
)

// Validate checks the structure of desired firmware without consulting any
// updater release. In a fleet template, profile, updater.name,
// updater.version and string values inside updater.config may contain
// template parameters, and only their syntax is checked here: which
// configuration fields may hold them is defined by the selected updater
// release. A standalone device must not contain template parameters.
func (f FirmwareSpec) Validate(fleetTemplate bool) []error {
	path := field.NewPath("spec", "firmware")
	var errs []error
	errs = append(errs, validateTemplatableFirmwareString(f.Profile, path.Child("profile"), fleetTemplate)...)
	if f.RetryGeneration != nil && *f.RetryGeneration < 0 {
		errs = append(errs, field.Invalid(path.Child("retryGeneration"), *f.RetryGeneration, "must be greater than or equal to 0"))
	}
	errs = append(errs, validateFirmwareUpdater(f.Updater, path.Child("updater"), fleetTemplate)...)

	// Status reports components and overlays in one list keyed by name, so
	// a name must be unique across both.
	names := map[string]struct{}{}
	errs = append(errs, validateFirmwareComponents(f.Components, path.Child("components"), names)...)
	errs = append(errs, validateFirmwareOverlays(f.Overlays, path.Child("overlays"), names)...)
	return errs
}

// UpdaterIdentityTemplated reports whether updater.name or updater.version
// contains template parameters. Such an updater release can only be resolved
// once the template is rendered for a device.
func (f FirmwareSpec) UpdaterIdentityTemplated() bool {
	return hasTemplateParameters(f.Updater.Name) || hasTemplateParameters(f.Updater.Version)
}

// Validate checks the structure of reported firmware status: enumerated
// values, the cancellation reason of canceled attempts and the required
// fields of firmware errors.
func (s FirmwareStatus) Validate() []error {
	path := field.NewPath("status", "firmware")
	var errs []error
	if s.Support.ApiVersion < 1 {
		errs = append(errs, field.Invalid(path.Child("support", "apiVersion"), s.Support.ApiVersion, "must be greater than or equal to 1"))
	}
	errs = append(errs, validateFirmwareRequiredStrings(path, map[string]string{"bootID": s.BootID})...)
	for i, updater := range s.Support.Updaters {
		errs = append(errs, validateFirmwareRequiredStrings(path.Child("support", "updaters").Index(i), map[string]string{
			"name": updater.Name, "version": updater.Version, "buildDigest": updater.BuildDigest, "schemaDigest": updater.SchemaDigest,
		})...)
	}
	if s.Attempt != nil {
		errs = append(errs, validateFirmwareAttempt(*s.Attempt, path.Child("attempt"))...)
	}
	names := map[string]struct{}{}
	for i, component := range s.Components {
		componentPath := path.Child("components").Index(i)
		errs = append(errs, validateFirmwareRequiredStrings(componentPath, map[string]string{"name": component.Name, "bootID": component.BootID})...)
		if component.Name != "" && !firmwareComponentNamePattern.MatchString(component.Name) {
			errs = append(errs, field.Invalid(componentPath.Child("name"), component.Name, firmwareNameDetail))
		}
		errs = append(errs, checkFirmwareNameUnique(component.Name, componentPath.Child("name"), names)...)
		if component.Selected != nil {
			source := component.Selected.Source
			errs = append(errs, validateFirmwareRequiredStrings(componentPath.Child("selected", "source"), map[string]string{"kind": source.Kind, "identity": source.Identity, "digest": source.Digest})...)
		}
		if component.Observation != nil && component.Observation.Backend != nil {
			backend := component.Observation.Backend
			errs = append(errs, validateFirmwareRequiredStrings(componentPath.Child("observation", "backend"), map[string]string{"updater": backend.Updater, "schemaDigest": backend.SchemaDigest})...)
		}
		errs = append(errs, validateFirmwareEnum(component.Type, firmwareComponentTypes, componentPath.Child("type"))...)
		errs = append(errs, validateFirmwareEnum(component.State, firmwareComponentStates, componentPath.Child("state"))...)
		if component.Error != nil {
			errs = append(errs, validateFirmwareError(*component.Error, componentPath.Child("error"))...)
		}
	}
	return errs
}

func hasTemplateParameters(s string) bool {
	return strings.Contains(s, "{{")
}

// validateTemplatableFirmwareString checks a required string that a fleet
// template may parameterize. A parameterized value is checked for template
// syntax only, because its own value is known only after rendering.
func validateTemplatableFirmwareString(value string, path *field.Path, fleetTemplate bool) []error {
	if !hasTemplateParameters(value) {
		if value == "" {
			return []error{field.Required(path, "")}
		}
		return nil
	}
	if !fleetTemplate {
		return []error{field.Invalid(path, value, firmwareStandaloneTemplateDetail)}
	}
	_, errs := validateParametersInString(&value, path.String(), true)
	return errs
}

// validateFixedFirmwareString checks a required string that must be literal.
func validateFixedFirmwareString(value string, path *field.Path) []error {
	if value == "" {
		return []error{field.Required(path, "")}
	}
	if hasTemplateParameters(value) {
		return []error{field.Invalid(path, value, firmwareFixedFieldTemplateDetail)}
	}
	return nil
}

// validateFirmwareVersionValue checks an optional version string that must be
// literal when it is set.
func validateFirmwareVersionValue(value *string, path *field.Path) []error {
	if value == nil {
		return nil
	}
	if *value == "" {
		return []error{field.Invalid(path, *value, "must not be empty")}
	}
	if hasTemplateParameters(*value) {
		return []error{field.Invalid(path, *value, firmwareFixedFieldTemplateDetail)}
	}
	return nil
}

func validateFirmwareUpdater(updater FirmwareUpdaterSpec, path *field.Path, fleetTemplate bool) []error {
	var errs []error
	errs = append(errs, validateTemplatableFirmwareString(updater.Name, path.Child("name"), fleetTemplate)...)
	errs = append(errs, validateTemplatableFirmwareString(updater.Version, path.Child("version"), fleetTemplate)...)
	if updater.Config == nil {
		return append(errs, field.Required(path.Child("config"), "must be a JSON object"))
	}
	return append(errs, validateFirmwareConfigTemplates(updater.Config, path.Child("config"), fleetTemplate)...)
}

type firmwareConfigTemplateValidator struct {
	fleetTemplate bool
	depth         int
	nodes         int
	errs          []error
}

// validateFirmwareConfigTemplates walks decoded updater configuration and
// checks every template parameter in it. Keys are never templatable, and
// standalone devices accept no template parameters at all.
func validateFirmwareConfigTemplates(value any, path *field.Path, fleetTemplate bool) []error {
	validator := &firmwareConfigTemplateValidator{fleetTemplate: fleetTemplate}
	validator.walk(value, path)
	return validator.errs
}

func (v *firmwareConfigTemplateValidator) full() bool {
	return len(v.errs) >= firmwareConfigMaxErrors
}

func (v *firmwareConfigTemplateValidator) addErrors(errs ...error) {
	for _, err := range errs {
		if err == nil || v.full() {
			continue
		}
		v.errs = append(v.errs, err)
	}
}

func (v *firmwareConfigTemplateValidator) walk(value any, path *field.Path) {
	if v.full() {
		return
	}
	v.nodes++
	if v.nodes > firmwareConfigMaxNodes {
		v.addErrors(field.Invalid(path, value, "firmware configuration must contain at most 10000 values"))
		return
	}
	if v.depth >= firmwareConfigMaxDepth {
		v.addErrors(field.Invalid(path, value, "firmware configuration must not nest deeper than 64 levels"))
		return
	}

	switch typedValue := value.(type) {
	case map[string]any:
		keys := lo.Keys(typedValue)
		slices.Sort(keys)
		v.depth++
		defer func() { v.depth-- }()
		for _, key := range keys {
			if v.full() || v.nodes > firmwareConfigMaxNodes {
				return
			}
			keyPath := path.Child(key)
			if hasTemplateParameters(key) {
				v.addErrors(field.Invalid(keyPath, key, firmwareConfigKeyTemplateDetail))
			}
			v.walk(typedValue[key], keyPath)
		}
	case []any:
		for i, item := range typedValue {
			if v.full() || v.nodes > firmwareConfigMaxNodes {
				return
			}
			v.walk(item, path.Index(i))
		}
	case string:
		if !hasTemplateParameters(typedValue) {
			return
		}
		if !v.fleetTemplate {
			v.addErrors(field.Invalid(path, typedValue, firmwareStandaloneTemplateDetail))
			return
		}
		_, errs := validateParametersInString(&typedValue, path.String(), true)
		v.addErrors(errs...)
	}
}

func validateFirmwareName(value string, path *field.Path) []error {
	errs := validateFixedFirmwareString(value, path)
	if len(errs) == 0 && !firmwareComponentNamePattern.MatchString(value) {
		errs = append(errs, field.Invalid(path, value, firmwareNameDetail))
	}
	return errs
}

func validateFirmwareComponents(components []FirmwareComponentSpec, path *field.Path, names map[string]struct{}) []error {
	if len(components) == 0 {
		return []error{field.Required(path, "at least one firmware component is required")}
	}
	var errs []error
	for i, component := range components {
		componentPath := path.Index(i)
		namePath := componentPath.Child("name")
		errs = append(errs, validateFirmwareName(component.Name, namePath)...)
		errs = append(errs, checkFirmwareNameUnique(component.Name, namePath, names)...)
		errs = append(errs, validateFirmwareVersionConstraints(component.Version, componentPath.Child("version"))...)

		if component.ArtifactDigest != nil {
			digestPath := componentPath.Child("artifactDigest")
			digest := *component.ArtifactDigest
			switch {
			case hasTemplateParameters(digest):
				errs = append(errs, field.Invalid(digestPath, digest, firmwareFixedFieldTemplateDetail))
			case !firmwareDigestPattern.MatchString(digest):
				errs = append(errs, field.Invalid(digestPath, digest, "must be a SHA-256 digest of the form sha256:<64 lowercase hexadecimal characters>"))
			}
		}
	}
	return errs
}

// validateFirmwareVersionConstraints checks the shape of version intent.
// Versions are not compared: firmware version semantics belong to the updater,
// so the service accepts combinations such as a minimum above the maximum.
func validateFirmwareVersionConstraints(constraints FirmwareVersionConstraints, path *field.Path) []error {
	var errs []error
	errs = append(errs, validateFirmwareVersionValue(constraints.Exact, path.Child("exact"))...)
	errs = append(errs, validateFirmwareVersionValue(constraints.Minimum, path.Child("minimum"))...)
	errs = append(errs, validateFirmwareVersionValue(constraints.Maximum, path.Child("maximum"))...)
	if constraints.Exact == nil && constraints.Minimum == nil && constraints.Maximum == nil && !lo.FromPtr(constraints.Latest) {
		errs = append(errs, field.Required(path, "set at least one of exact, minimum or maximum, or set latest to true"))
	}
	return errs
}

func validateFirmwareOverlays(overlays []FirmwareOverlaySpec, path *field.Path, names map[string]struct{}) []error {
	if overlays == nil {
		return []error{field.Required(path, "the list may be empty")}
	}
	var errs []error
	for i, overlay := range overlays {
		overlayPath := path.Index(i)
		namePath := overlayPath.Child("name")
		errs = append(errs, validateFirmwareName(overlay.Name, namePath)...)
		errs = append(errs, checkFirmwareNameUnique(overlay.Name, namePath, names)...)

		compatibilityPath := overlayPath.Child("compatibleFirmware")
		if len(overlay.CompatibleFirmware) == 0 {
			errs = append(errs, field.Required(compatibilityPath, "at least one compatible firmware entry is required"))
			continue
		}
		for j, compatibility := range overlay.CompatibleFirmware {
			entryPath := compatibilityPath.Index(j)
			errs = append(errs, validateFixedFirmwareString(compatibility.Component, entryPath.Child("component"))...)
			errs = append(errs, validateFirmwareVersionValue(compatibility.Exact, entryPath.Child("exact"))...)
			errs = append(errs, validateFirmwareVersionValue(compatibility.Minimum, entryPath.Child("minimum"))...)
			errs = append(errs, validateFirmwareVersionValue(compatibility.Maximum, entryPath.Child("maximum"))...)
		}
	}
	return errs
}

func checkFirmwareNameUnique(name string, path *field.Path, names map[string]struct{}) []error {
	if name == "" {
		return nil
	}
	if _, seen := names[name]; seen {
		return []error{field.Duplicate(path, name)}
	}
	names[name] = struct{}{}
	return nil
}

func validateFirmwareAttempt(attempt FirmwareAttemptStatus, path *field.Path) []error {
	var errs []error
	errs = append(errs, validateFirmwareRequiredStrings(path, map[string]string{"id": attempt.Id, "renderedVersion": attempt.RenderedVersion, "specHash": attempt.SpecHash})...)
	if attempt.Updater != nil {
		u := attempt.Updater
		errs = append(errs, validateFirmwareRequiredStrings(path.Child("updater"), map[string]string{"name": u.Name, "version": u.Version, "buildDigest": u.BuildDigest, "schemaDigest": u.SchemaDigest, "configDigest": u.ConfigDigest})...)
	}
	for i, action := range attempt.Recovery {
		errs = append(errs, validateFirmwareRequiredStrings(path.Child("recovery").Index(i), map[string]string{"code": action.Code, "message": action.Message})...)
	}
	errs = append(errs, validateFirmwareEnum(attempt.Phase, firmwareAttemptPhases, path.Child("phase"))...)
	if attempt.RetryGeneration < 0 {
		errs = append(errs, field.Invalid(path.Child("retryGeneration"), attempt.RetryGeneration, "must be greater than or equal to 0"))
	}
	reasonPath := path.Child("cancellationReason")
	switch {
	case attempt.CancellationReason != nil:
		errs = append(errs, validateFirmwareEnum(*attempt.CancellationReason, firmwareCancellationReasons, reasonPath)...)
	case attempt.Phase == FirmwareAttemptPhaseCanceled:
		errs = append(errs, field.Required(reasonPath, "required when the attempt phase is Canceled"))
	}
	if attempt.Error != nil {
		errs = append(errs, validateFirmwareError(*attempt.Error, path.Child("error"))...)
	}
	return errs
}

// validateFirmwareError checks the required fields of a firmware error and
// that it names an exceeded budget exactly when its reason is
// ResourceLimitExceeded. Whether the named budget exists depends on the
// updater release and is checked by the service.
func validateFirmwareError(firmwareErr FirmwareError, path *field.Path) []error {
	var errs []error
	if firmwareErr.Reason == "" {
		errs = append(errs, field.Required(path.Child("reason"), ""))
	}
	if firmwareErr.Message == "" {
		errs = append(errs, field.Required(path.Child("message"), ""))
	}
	if firmwareErr.Phase != nil {
		errs = append(errs, validateFirmwareEnum(*firmwareErr.Phase, firmwareAttemptPhases, path.Child("phase"))...)
	}
	budgetPath := path.Child("exceededBudget")
	switch {
	case firmwareErr.Reason == "":
		// Without a reason, the budget rule cannot apply; the missing reason is reported above.
	case firmwareErr.Reason == FirmwareErrorReasonResourceLimitExceeded && lo.FromPtr(firmwareErr.ExceededBudget) == "":
		errs = append(errs, field.Required(budgetPath, "a "+FirmwareErrorReasonResourceLimitExceeded+" error must name the exceeded budget"))
	case firmwareErr.Reason != FirmwareErrorReasonResourceLimitExceeded && firmwareErr.ExceededBudget != nil:
		errs = append(errs, field.Forbidden(budgetPath, "only a "+FirmwareErrorReasonResourceLimitExceeded+" error may name an exceeded budget"))
	}
	return errs
}

func validateFirmwareEnum[T ~string](value T, allowed []T, path *field.Path) []error {
	if value == "" {
		return []error{field.Required(path, "")}
	}
	if !slices.Contains(allowed, value) {
		return []error{field.NotSupported(path, value, allowed)}
	}
	return nil
}

func validateFirmwareRequiredStrings(path *field.Path, values map[string]string) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if values[name] == "" {
			errs = append(errs, field.Required(path.Child(name), ""))
		}
	}
	return errs
}

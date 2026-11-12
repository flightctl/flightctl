package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ErrApprovedDescriptorsUnavailable reports that no source of approved
// firmware updater releases is available, so firmware that needs one cannot
// be validated.
var ErrApprovedDescriptorsUnavailable = errors.New("approved firmware updater releases are unavailable")

// ApprovedDescriptorProvider resolves approved firmware updater releases.
type ApprovedDescriptorProvider interface {
	// LookupRelease returns the descriptor of the updater release with the
	// given concrete name and version. Approved reports whether the release
	// is currently authorized. A nil descriptor with a nil error means the
	// release is unknown; an error means the lookup itself failed.
	LookupRelease(ctx context.Context, name, version string) (*ApprovedDescriptor, error)

	// LookupSchemaByDigest returns retained observation schema and budget
	// evidence keyed by updater name and either a pinned configuration digest
	// or an observation digest. Shared keys must have identical evidence. The evidence stays available after a
	// release loses its authorization, and it never authorizes execution.
	LookupSchemaByDigest(ctx context.Context, updater, schemaDigest string) (*RetainedSchemaEvidence, error)
}

// ApprovedDescriptor is the immutable description of one updater release.
type ApprovedDescriptor struct {
	// Name and Version identify the release the descriptor describes.
	Name    string
	Version string
	// Approved reports whether the release is currently authorized.
	Approved bool
	// BuildDigest identifies the immutable updater delivery image.
	BuildDigest string
	// SchemaDigest identifies ConfigSchema.
	SchemaDigest string
	// ConfigSchema is the closed JSON Schema for updater.config. It is
	// validated as supplied and never loads external schemas.
	ConfigSchema json.RawMessage
	// ObservationSchemaDigest identifies the exact observation schema bytes.
	ObservationSchemaDigest string
	// ObservationSchema is the JSON Schema for observation backend data.
	ObservationSchema json.RawMessage
	// ReferencePaths are the configuration paths that hold repository names.
	ReferencePaths []FirmwareReferencePath
	// GUIDPaths are the configuration paths that hold target GUIDs.
	GUIDPaths []string
	// TemplatablePaths are the string configuration paths that a fleet
	// template may fill with template parameters.
	TemplatablePaths []string
	// BudgetKeySet lists the valid resource budget names of the release.
	BudgetKeySet []string
}

// FirmwareReferenceKind is the role of a repository reference in updater
// configuration.
type FirmwareReferenceKind string

const (
	FirmwareReferenceArtifact  FirmwareReferenceKind = "artifact"
	FirmwareReferenceSignature FirmwareReferenceKind = "signature"
)

// FirmwareReferencePath locates repository names in updater configuration.
//
// Path names the location as dot-separated object members. A member followed
// by "[]" steps into every element of the array it holds, for example
// "releases[].artifact.source.repository". Members are matched exactly; there
// is no wildcard or recursive search. TemplatablePaths and GUIDPaths use the
// same notation.
type FirmwareReferencePath struct {
	Path string
	Kind FirmwareReferenceKind
}

// RetainedSchemaEvidence is the immutable observation schema of an updater
// release, retained independently of its current authorization.
type RetainedSchemaEvidence struct {
	SchemaDigest      string
	ObservationSchema json.RawMessage
	BudgetKeySet      []string
}

// UnavailableDescriptorProvider is the ApprovedDescriptorProvider of a service
// without a source of approved updater releases. Every lookup fails with
// ErrApprovedDescriptorsUnavailable, so firmware that needs an approved
// release is rejected rather than accepted unchecked.
type UnavailableDescriptorProvider struct{}

func (UnavailableDescriptorProvider) LookupRelease(context.Context, string, string) (*ApprovedDescriptor, error) {
	return nil, ErrApprovedDescriptorsUnavailable
}

func (UnavailableDescriptorProvider) LookupSchemaByDigest(context.Context, string, string) (*RetainedSchemaEvidence, error) {
	return nil, ErrApprovedDescriptorsUnavailable
}

// FirmwareValidationMode tells whether firmware belongs to a standalone device,
// whose values must be concrete, or to a fleet template.
type FirmwareValidationMode int

const (
	FirmwareStandalone FirmwareValidationMode = iota
	FirmwareFleetTemplate
)

// Inputs that a deferred firmware check waits for.
const (
	FirmwareDependsOnTemplateValue   = "template-value"
	FirmwareDependsOnUpdaterIdentity = "updater-identity"
)

// DeferredFirmwareCheck is a check that cannot run until a fleet template is
// rendered for a device, because its input is still a template parameter.
type DeferredFirmwareCheck struct {
	// Path is the location of the unresolved input.
	Path string
	// Check names the pending check, for example "format:uuid".
	Check string
	// DependsOn names the unresolved input.
	DependsOn string
}

// FirmwareAdmission is the outcome of firmware intent validation.
type FirmwareAdmission struct {
	// Errors are the field-specific validation errors of a rejected request.
	Errors []error
	// Deferred lists the checks of an accepted fleet template that wait for
	// template rendering.
	Deferred []DeferredFirmwareCheck
	// ConcreteRepositoryRefs are the sorted names of the literal artifact and
	// signature repositories that were found in the organization.
	ConcreteRepositoryRefs []string
}

// RepositoryGetter returns the Repository with the given name in an
// organization, reporting a missing one with a not-found status, as the
// repository service's GetRepository does.
type RepositoryGetter func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status)

const (
	firmwareUpdaterPath     = "spec.firmware.updater"
	firmwareConfigPath      = "spec.firmware.updater.config"
	firmwareConfigSchemaURL = "file:///firmware-updater-config.json"

	checkReleaseAuthorization = "release-authorization"
	checkConfigSchema         = "config-schema"
	checkTemplatePlacement    = "template-placement"
	checkTargetGUID           = "target-guid"
	checkDuplicateTarget      = "duplicate-target"
	checkRepositoryReference  = "repository-reference"
	checkConfigValue          = "config-value"
)

var (
	firmwareGUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// deferrableSchemaKeywords are the keywords that constrain a string value
	// itself. Only failures of these keywords on a templated string can wait
	// for rendering; type, required and unknown-field failures cannot.
	deferrableSchemaKeywords = map[string]bool{
		"pattern": true, "format": true, "enum": true, "const": true, "minLength": true, "maxLength": true,
	}
)

// ValidateFirmwareIntent validates the desired firmware of a device spec
// against the approved updater release it selects, before the spec is stored.
//
// Every check whose inputs are concrete runs now: the structural checks, the
// release lookup and its authorization, the release's closed configuration
// schema, template placement, target GUIDs and the tenant-scoped repository
// references. In a fleet template, only checks that depend on a template
// parameter are deferred: when the updater name or version is templated, no
// release is looked up and every release-dependent check is deferred;
// otherwise only failures of templated values at paths the release designates
// as templatable are deferred. A standalone device accepts no templates.
//
// A rejected request returns its errors and neither deferred checks nor
// repository references. A missing provider or release information fails
// closed. The function does not modify the spec.
func ValidateFirmwareIntent(ctx context.Context, orgId uuid.UUID, provider ApprovedDescriptorProvider, getRepository RepositoryGetter,
	spec *domain.DeviceSpec, mode FirmwareValidationMode) (FirmwareAdmission, domain.Status) {
	if spec == nil || spec.Firmware == nil {
		return FirmwareAdmission{}, domain.StatusOK()
	}
	firmware := spec.Firmware
	if errs := firmware.Validate(mode == FirmwareFleetTemplate); len(errs) > 0 {
		return rejectFirmware(errs)
	}
	if firmware.UpdaterIdentityTemplated() {
		return FirmwareAdmission{Deferred: updaterIdentityDeferrals()}, domain.StatusOK()
	}

	name, version := firmware.Updater.Name, firmware.Updater.Version
	descriptor, err := resolveFirmwareRelease(ctx, provider, name, version)
	switch {
	case errors.Is(err, ErrApprovedDescriptorsUnavailable):
		return rejectFirmware([]error{fmt.Errorf("%s: approved updater releases are unavailable, so the firmware cannot be validated", firmwareUpdaterPath)})
	case err != nil:
		return FirmwareAdmission{}, domain.StatusInternalServerError(err.Error())
	case descriptor == nil:
		return rejectFirmware([]error{fmt.Errorf("%s: updater release %s/%s not found", firmwareUpdaterPath, name, version)})
	case !descriptor.Approved:
		return rejectFirmware([]error{fmt.Errorf("%s: updater release %s/%s is not authorized", firmwareUpdaterPath, name, version)})
	}

	release, err := compileFirmwareDescriptor(descriptor)
	if err != nil {
		return FirmwareAdmission{}, domain.StatusInternalServerError(fmt.Sprintf("approved updater release %s/%s has an invalid descriptor: %v", name, version, err))
	}
	config, err := decodeFirmwareConfig(firmware.Updater.Config)
	if err != nil {
		return FirmwareAdmission{}, domain.StatusInternalServerError(fmt.Sprintf("failed to decode %s: %v", firmwareConfigPath, err))
	}

	check := &firmwareConfigCheck{release: release, config: config}
	if mode == FirmwareFleetTemplate {
		check.rejectMisplacedTemplates()
	}
	if err := check.checkSchema(); err != nil {
		return FirmwareAdmission{}, domain.StatusInternalServerError(err.Error())
	}
	check.checkTargetGUIDs()
	if len(check.errs) > 0 {
		return rejectFirmware(check.errs)
	}

	refs, status := check.checkRepositoryReferences(ctx, orgId, getRepository)
	if status.Code != domain.StatusOK().Code {
		return FirmwareAdmission{}, status
	}
	if len(check.errs) > 0 {
		return rejectFirmware(check.errs)
	}
	check.deferRemainingTemplates()
	return FirmwareAdmission{Deferred: check.sortedDeferred(), ConcreteRepositoryRefs: refs}, domain.StatusOK()
}

// ExtractFirmwareRepositoryRefs returns the literal artifact and signature
// repository names in desired firmware, as declared by the descriptor of its
// updater release, for dependency tracking. It does not authorize them, and
// it reads the descriptor of a release whether or not it is still approved.
//
// complete is false when the release has a concrete identity but its
// descriptor is unknown or unavailable, so the references cannot be known.
// A fleet template with a templated updater identity has no firmware
// references until it is rendered. Templated reference values are never
// returned.
func ExtractFirmwareRepositoryRefs(ctx context.Context, provider ApprovedDescriptorProvider, firmware *domain.FirmwareSpec,
	mode FirmwareValidationMode) (refs []string, complete bool, err error) {
	if firmware == nil {
		return nil, true, nil
	}
	if firmware.UpdaterIdentityTemplated() {
		if mode != FirmwareFleetTemplate {
			return nil, false, fmt.Errorf("%s: template parameters are only supported in fleet templates", firmwareUpdaterPath)
		}
		return nil, true, nil
	}

	name, version := firmware.Updater.Name, firmware.Updater.Version
	descriptor, err := resolveFirmwareRelease(ctx, provider, name, version)
	switch {
	case errors.Is(err, ErrApprovedDescriptorsUnavailable):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case descriptor == nil:
		return nil, false, nil
	}
	references, err := parseFirmwareReferencePaths(descriptor.ReferencePaths)
	if err != nil {
		return nil, false, fmt.Errorf("approved updater release %s/%s has an invalid descriptor: %w", name, version, err)
	}
	config, err := decodeFirmwareConfig(firmware.Updater.Config)
	if err != nil {
		return nil, false, fmt.Errorf("failed to decode %s: %w", firmwareConfigPath, err)
	}

	names := map[string]struct{}{}
	for _, reference := range references {
		for _, found := range collectFirmwareConfigValues(config, reference, nil) {
			if value, ok := found.value.(string); ok && value != "" && !containsFirmwareTemplate(value) {
				names[value] = struct{}{}
			}
		}
	}
	return sortedNames(names), true, nil
}

func rejectFirmware(errs []error) (FirmwareAdmission, domain.Status) {
	return FirmwareAdmission{Errors: errs}, domain.StatusBadRequest(errors.Join(errs...).Error())
}

// updaterIdentityDeferrals lists the checks that need the descriptor of an
// updater release whose name or version is still a template parameter.
func updaterIdentityDeferrals() []DeferredFirmwareCheck {
	return []DeferredFirmwareCheck{
		{Path: firmwareUpdaterPath, Check: checkReleaseAuthorization, DependsOn: FirmwareDependsOnUpdaterIdentity},
		{Path: firmwareConfigPath, Check: checkConfigSchema, DependsOn: FirmwareDependsOnUpdaterIdentity},
		{Path: firmwareConfigPath, Check: checkRepositoryReference, DependsOn: FirmwareDependsOnUpdaterIdentity},
		{Path: firmwareConfigPath, Check: checkTargetGUID, DependsOn: FirmwareDependsOnUpdaterIdentity},
		{Path: firmwareConfigPath, Check: checkTemplatePlacement, DependsOn: FirmwareDependsOnUpdaterIdentity},
	}
}

// resolveFirmwareRelease looks up the descriptor of a concrete updater
// release. A nil descriptor with a nil error means the release is unknown.
func resolveFirmwareRelease(ctx context.Context, provider ApprovedDescriptorProvider, name, version string) (*ApprovedDescriptor, error) {
	if provider == nil {
		return nil, ErrApprovedDescriptorsUnavailable
	}
	descriptor, err := provider.LookupRelease(ctx, name, version)
	if errors.Is(err, ErrApprovedDescriptorsUnavailable) {
		return nil, ErrApprovedDescriptorsUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up approved updater release %s/%s: %w", name, version, err)
	}
	if descriptor != nil && (descriptor.Name != name || descriptor.Version != version) {
		return nil, fmt.Errorf("approved updater release lookup for %s/%s returned a different release", name, version)
	}
	return descriptor, nil
}

// firmwareRelease is a compiled approved descriptor.
type firmwareRelease struct {
	label       string
	schema      *jsonschema.Schema
	schemaDoc   any
	templatable []firmwarePathPattern
	guids       []firmwarePathPattern
	references  []firmwarePathPattern
}

func compileFirmwareDescriptor(descriptor *ApprovedDescriptor) (*firmwareRelease, error) {
	if len(bytes.TrimSpace(descriptor.ConfigSchema)) == 0 {
		return nil, errors.New("the configuration schema is missing")
	}
	compiler := jsonschema.NewCompiler()
	// Format is an annotation by default under the current draft; asserting
	// it makes formats such as uuid reject malformed values.
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	if err := compiler.AddResource(firmwareConfigSchemaURL, bytes.NewReader(descriptor.ConfigSchema)); err != nil {
		return nil, errors.New("the configuration schema is not valid JSON")
	}
	schema, err := compiler.Compile(firmwareConfigSchemaURL)
	if err != nil {
		return nil, errors.New("the configuration schema is not a valid JSON Schema")
	}
	schemaDoc, err := decodeJSONWithNumbers(descriptor.ConfigSchema)
	if err != nil {
		return nil, errors.New("the configuration schema is not valid JSON")
	}

	release := &firmwareRelease{
		label:     descriptor.Name + "/" + descriptor.Version,
		schema:    schema,
		schemaDoc: schemaDoc,
	}
	if release.templatable, err = parseFirmwarePathPatterns(descriptor.TemplatablePaths); err != nil {
		return nil, fmt.Errorf("templatable path: %w", err)
	}
	if release.guids, err = parseFirmwarePathPatterns(descriptor.GUIDPaths); err != nil {
		return nil, fmt.Errorf("GUID path: %w", err)
	}
	if release.references, err = parseFirmwareReferencePaths(descriptor.ReferencePaths); err != nil {
		return nil, err
	}
	return release, nil
}

func parseFirmwareReferencePaths(paths []FirmwareReferencePath) ([]firmwarePathPattern, error) {
	patterns := make([]firmwarePathPattern, 0, len(paths))
	for _, path := range paths {
		if path.Kind != FirmwareReferenceArtifact && path.Kind != FirmwareReferenceSignature {
			return nil, fmt.Errorf("reference path %q has unsupported kind %q", path.Path, path.Kind)
		}
		pattern, err := parseFirmwarePathPattern(path.Path)
		if err != nil {
			return nil, fmt.Errorf("reference path: %w", err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// firmwareConfigCheck accumulates the results of validating decoded updater
// configuration against a compiled release.
type firmwareConfigCheck struct {
	release *firmwareRelease
	config  any

	errs            []error
	deferred        []DeferredFirmwareCheck
	schemaErrorAt   map[string]bool
	schemaDeferrals map[string]bool
}

// rejectMisplacedTemplates rejects template parameters at configuration paths
// that the release does not designate as templatable.
func (c *firmwareConfigCheck) rejectMisplacedTemplates() {
	for _, leaf := range collectFirmwareTemplateLeaves(c.config, nil) {
		if !c.release.isTemplatable(leaf.location) {
			c.errs = append(c.errs, field.Invalid(firmwareConfigFieldPath(leaf.location), leaf.value,
				fmt.Sprintf("template parameters are not permitted at this path for updater release %s", c.release.label)))
		}
	}
}

// checkSchema validates the configuration against the release's closed
// schema. Failures of templated values that may only be checked after
// rendering are deferred; every other failure is reported.
func (c *firmwareConfigCheck) checkSchema() error {
	c.schemaErrorAt = map[string]bool{}
	c.schemaDeferrals = map[string]bool{}
	err := c.release.schema.Validate(c.config)
	if err == nil {
		return nil
	}
	var root *jsonschema.ValidationError
	if !errors.As(err, &root) {
		return fmt.Errorf("failed to validate %s: %w", firmwareConfigPath, err)
	}

	retained, deferredLeaves := c.pruneSchemaErrors(root, true)
	if retained != nil {
		var failures []*jsonschema.ValidationError
		collectSchemaFailures(retained, true, &failures)
		for _, failure := range failures {
			path := c.instancePath(failure.InstanceLocation)
			c.schemaErrorAt[path] = true
			c.errs = append(c.errs, fmt.Errorf("%s: %s", path, failure.Message))
		}
		return nil
	}
	for _, leaf := range deferredLeaves {
		path := c.instancePath(leaf.InstanceLocation)
		c.schemaDeferrals[path] = true
		c.deferred = append(c.deferred, DeferredFirmwareCheck{Path: path, Check: c.deferredCheckName(leaf), DependsOn: FirmwareDependsOnTemplateValue})
	}
	return nil
}

// pruneSchemaErrors walks the error tree returned by the schema and rebuilds
// it without the failures that can wait for rendering, without modifying the
// original tree.
//
// A leaf is deferred only when it is a failure of a deferrable keyword on a
// templated string at a templatable path. A wrapper whose failure is exactly
// the conjunction of its causes (the root, an aggregation of several failures,
// a reference or an allOf branch) disappears when every failure beneath it is
// deferred, and is otherwise rebuilt around the remaining failures. Any other
// node with causes, such as anyOf, oneOf, then, else or minContains, judges
// alternatives, a condition or a count, so a deferred cause does not satisfy
// it: such a node is kept whole.
func (c *firmwareConfigCheck) pruneSchemaErrors(node *jsonschema.ValidationError, root bool) (*jsonschema.ValidationError, []*jsonschema.ValidationError) {
	if len(node.Causes) == 0 {
		if c.isDeferrableFailure(node) {
			return nil, []*jsonschema.ValidationError{node}
		}
		return node, nil
	}
	if !root && !isDependentSchemaWrapper(node) {
		return node, nil
	}
	var kept, deferredLeaves []*jsonschema.ValidationError
	for _, cause := range node.Causes {
		keptCause, causeDeferred := c.pruneSchemaErrors(cause, false)
		deferredLeaves = append(deferredLeaves, causeDeferred...)
		if keptCause != nil {
			kept = append(kept, keptCause)
		}
	}
	if len(kept) == 0 {
		return nil, deferredLeaves
	}
	rebuilt := *node
	rebuilt.Causes = kept
	return &rebuilt, deferredLeaves
}

// isDependentSchemaWrapper reports whether a schema error node adds no
// constraint of its own, so that it fails exactly when its causes fail.
func isDependentSchemaWrapper(node *jsonschema.ValidationError) bool {
	if node.Message == "" {
		return true
	}
	tokens := schemaKeywordTokens(node.AbsoluteKeywordLocation)
	n := len(tokens)
	if n == 0 {
		return false
	}
	switch tokens[n-1] {
	case "$ref", "$recursiveRef", "$dynamicRef":
		return true
	}
	return n >= 2 && tokens[n-2] == "allOf" && isArrayIndexToken(tokens[n-1])
}

func collectSchemaFailures(node *jsonschema.ValidationError, root bool, out *[]*jsonschema.ValidationError) {
	if len(node.Causes) == 0 || (!root && !isDependentSchemaWrapper(node)) {
		*out = append(*out, node)
		return
	}
	for _, cause := range node.Causes {
		collectSchemaFailures(cause, false, out)
	}
}

func (c *firmwareConfigCheck) isDeferrableFailure(node *jsonschema.ValidationError) bool {
	tokens := schemaKeywordTokens(node.AbsoluteKeywordLocation)
	if len(tokens) == 0 || !deferrableSchemaKeywords[tokens[len(tokens)-1]] {
		return false
	}
	location, value, ok := resolveFirmwareInstance(c.config, node.InstanceLocation)
	if !ok {
		return false
	}
	text, isString := value.(string)
	return isString && containsFirmwareTemplate(text) && c.release.isTemplatable(location)
}

// deferredCheckName names a deferred schema failure by its keyword, adding the
// format name for a format failure.
func (c *firmwareConfigCheck) deferredCheckName(leaf *jsonschema.ValidationError) string {
	tokens := schemaKeywordTokens(leaf.AbsoluteKeywordLocation)
	keyword := tokens[len(tokens)-1]
	if keyword != "format" {
		return keyword
	}
	base, _, _ := strings.Cut(leaf.AbsoluteKeywordLocation, "#")
	if base != firmwareConfigSchemaURL {
		return keyword
	}
	current := c.release.schemaDoc
	for _, token := range tokens {
		switch node := current.(type) {
		case map[string]any:
			current = node[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return keyword
			}
			current = node[index]
		default:
			return keyword
		}
	}
	if format, ok := current.(string); ok && format != "" {
		return keyword + ":" + format
	}
	return keyword
}

// checkTargetGUIDs checks the values at the release's GUID paths: literal
// values must be GUIDs and no two may target the same GUID. Checks that
// involve a templated value are deferred.
func (c *firmwareConfigCheck) checkTargetGUIDs() {
	type guidValue struct {
		location []firmwarePathStep
		literal  string
	}
	var literals []guidValue
	var templated [][]firmwarePathStep
	for _, pattern := range c.release.guids {
		for _, found := range collectFirmwareConfigValues(c.config, pattern, nil) {
			path := firmwareConfigFieldPath(found.location)
			text, ok := found.value.(string)
			switch {
			case !ok:
				if !c.schemaErrorAt[path.String()] {
					c.errs = append(c.errs, field.Invalid(path, found.value, "must be a GUID string"))
				}
			case containsFirmwareTemplate(text):
				templated = append(templated, found.location)
				if !c.schemaDeferrals[path.String()] {
					c.deferred = append(c.deferred, DeferredFirmwareCheck{Path: path.String(), Check: checkTargetGUID, DependsOn: FirmwareDependsOnTemplateValue})
				}
			case !firmwareGUIDPattern.MatchString(text):
				if !c.schemaErrorAt[path.String()] {
					c.errs = append(c.errs, field.Invalid(path, text, "must be a GUID"))
				}
			default:
				literals = append(literals, guidValue{location: found.location, literal: strings.ToLower(text)})
			}
		}
	}

	firstUse := map[string]string{}
	for _, value := range literals {
		path := firmwareConfigFieldPath(value.location)
		if previous, duplicate := firstUse[value.literal]; duplicate {
			c.errs = append(c.errs, field.Invalid(path, value.literal, fmt.Sprintf("duplicate target GUID, also used at %s", previous)))
			continue
		}
		firstUse[value.literal] = path.String()
	}
	if len(literals)+len(templated) > 1 {
		for _, location := range templated {
			c.deferred = append(c.deferred, DeferredFirmwareCheck{
				Path: firmwareConfigFieldPath(location).String(), Check: checkDuplicateTarget, DependsOn: FirmwareDependsOnTemplateValue,
			})
		}
	}
}

// checkRepositoryReferences looks up the literal repository names at the
// release's reference paths in the organization. Templated names are deferred
// and never looked up. A failed lookup returns an error status; a missing or
// unusable repository adds a field error.
func (c *firmwareConfigCheck) checkRepositoryReferences(ctx context.Context, orgId uuid.UUID, getRepository RepositoryGetter) ([]string, domain.Status) {
	type reference struct {
		path *field.Path
		name string
	}
	var references []reference
	names := map[string]struct{}{}
	for _, pattern := range c.release.references {
		for _, found := range collectFirmwareConfigValues(c.config, pattern, nil) {
			path := firmwareConfigFieldPath(found.location)
			name, ok := found.value.(string)
			switch {
			case !ok:
				c.errs = append(c.errs, field.Invalid(path, found.value, "must be a repository name"))
			case containsFirmwareTemplate(name):
				c.deferred = append(c.deferred, DeferredFirmwareCheck{Path: path.String(), Check: checkRepositoryReference, DependsOn: FirmwareDependsOnTemplateValue})
			default:
				references = append(references, reference{path: path, name: name})
				names[name] = struct{}{}
			}
		}
	}
	if len(names) == 0 {
		return nil, domain.StatusOK()
	}
	if getRepository == nil {
		return nil, domain.StatusInternalServerError("repository lookup is unavailable, so firmware repository references cannot be validated")
	}

	problems := map[string]string{}
	sorted := sortedNames(names)
	for _, name := range sorted {
		repository, status := getRepository(ctx, orgId, name)
		switch {
		case status.Code == http.StatusNotFound:
			problems[name] = "repository not found"
		case status.Code != http.StatusOK:
			return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to look up repository %q: %s", name, status.Message))
		case repository == nil:
			return nil, domain.StatusInternalServerError(fmt.Sprintf("looking up repository %q returned no repository", name))
		default:
			if _, err := repository.Spec.GetRepoURL(); err != nil {
				problems[name] = "repository has no usable URL"
			}
		}
	}
	for _, ref := range references {
		if problem, failed := problems[ref.name]; failed {
			c.errs = append(c.errs, field.Invalid(ref.path, ref.name, problem))
		}
	}
	return sorted, domain.StatusOK()
}

// deferRemainingTemplates lists every templated value that no other deferred
// check covers, because its value is only validated after rendering.
func (c *firmwareConfigCheck) deferRemainingTemplates() {
	covered := map[string]bool{}
	for _, check := range c.deferred {
		covered[check.Path] = true
	}
	for _, leaf := range collectFirmwareTemplateLeaves(c.config, nil) {
		path := firmwareConfigFieldPath(leaf.location).String()
		if !covered[path] {
			c.deferred = append(c.deferred, DeferredFirmwareCheck{Path: path, Check: checkConfigValue, DependsOn: FirmwareDependsOnTemplateValue})
		}
	}
}

func (c *firmwareConfigCheck) sortedDeferred() []DeferredFirmwareCheck {
	deferred := slices.Clone(c.deferred)
	slices.SortFunc(deferred, func(a, b DeferredFirmwareCheck) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.Check, b.Check)
	})
	return slices.Compact(deferred)
}

func (c *firmwareConfigCheck) instancePath(pointer string) string {
	location, _, ok := resolveFirmwareInstance(c.config, pointer)
	if !ok {
		return firmwareConfigPath + pointer
	}
	return firmwareConfigFieldPath(location).String()
}

func (r *firmwareRelease) isTemplatable(location []firmwarePathStep) bool {
	return slices.ContainsFunc(r.templatable, func(pattern firmwarePathPattern) bool { return pattern.matches(location) })
}

// firmwarePathStep is one step through decoded configuration: an object
// member or an array element. In a firmwarePathPattern, an element step
// matches any index.
type firmwarePathStep struct {
	key     string
	index   int
	element bool
}

type firmwarePathPattern []firmwarePathStep

func parseFirmwarePathPatterns(paths []string) ([]firmwarePathPattern, error) {
	patterns := make([]firmwarePathPattern, 0, len(paths))
	for _, path := range paths {
		pattern, err := parseFirmwarePathPattern(path)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

func parseFirmwarePathPattern(path string) (firmwarePathPattern, error) {
	var pattern firmwarePathPattern
	for _, segment := range strings.Split(path, ".") {
		member := segment
		elements := 0
		for strings.HasSuffix(member, "[]") {
			member = strings.TrimSuffix(member, "[]")
			elements++
		}
		if member == "" || strings.ContainsAny(member, "[]") {
			return nil, fmt.Errorf("invalid path %q", path)
		}
		pattern = append(pattern, firmwarePathStep{key: member})
		for i := 0; i < elements; i++ {
			pattern = append(pattern, firmwarePathStep{element: true})
		}
	}
	return pattern, nil
}

func (p firmwarePathPattern) matches(location []firmwarePathStep) bool {
	if len(p) != len(location) {
		return false
	}
	for i, step := range p {
		if step.element != location[i].element || (!step.element && step.key != location[i].key) {
			return false
		}
	}
	return true
}

type firmwareConfigValue struct {
	location []firmwarePathStep
	value    any
}

// collectFirmwareConfigValues returns the values at every location matching
// pattern. Locations that do not exist are skipped.
func collectFirmwareConfigValues(value any, pattern firmwarePathPattern, location []firmwarePathStep) []firmwareConfigValue {
	if len(pattern) == 0 {
		return []firmwareConfigValue{{location: location, value: value}}
	}
	step := pattern[0]
	var found []firmwareConfigValue
	if step.element {
		items, _ := value.([]any)
		for i, item := range items {
			found = append(found, collectFirmwareConfigValues(item, pattern[1:], appendStep(location, firmwarePathStep{index: i, element: true}))...)
		}
		return found
	}
	object, _ := value.(map[string]any)
	child, ok := object[step.key]
	if !ok {
		return nil
	}
	return collectFirmwareConfigValues(child, pattern[1:], appendStep(location, firmwarePathStep{key: step.key}))
}

// collectFirmwareTemplateLeaves returns every string value that contains
// template parameters, in a stable order.
func collectFirmwareTemplateLeaves(value any, location []firmwarePathStep) []firmwareConfigValue {
	var found []firmwareConfigValue
	switch v := value.(type) {
	case map[string]any:
		keys := lo.Keys(v)
		slices.Sort(keys)
		for _, key := range keys {
			found = append(found, collectFirmwareTemplateLeaves(v[key], appendStep(location, firmwarePathStep{key: key}))...)
		}
	case []any:
		for i, item := range v {
			found = append(found, collectFirmwareTemplateLeaves(item, appendStep(location, firmwarePathStep{index: i, element: true}))...)
		}
	case string:
		if containsFirmwareTemplate(v) {
			found = append(found, firmwareConfigValue{location: location, value: v})
		}
	}
	return found
}

func appendStep(location []firmwarePathStep, step firmwarePathStep) []firmwarePathStep {
	return append(slices.Clip(location), step)
}

// resolveFirmwareInstance follows a JSON pointer, as reported by the schema
// validator, through decoded configuration.
func resolveFirmwareInstance(config any, pointer string) ([]firmwarePathStep, any, bool) {
	if pointer == "" {
		return nil, config, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, nil, false
	}
	var location []firmwarePathStep
	current := config
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch v := current.(type) {
		case map[string]any:
			child, ok := v[token]
			if !ok {
				return nil, nil, false
			}
			location = append(location, firmwarePathStep{key: token})
			current = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(v) {
				return nil, nil, false
			}
			location = append(location, firmwarePathStep{index: index, element: true})
			current = v[index]
		default:
			return nil, nil, false
		}
	}
	return location, current, true
}

func firmwareConfigFieldPath(location []firmwarePathStep) *field.Path {
	path := field.NewPath("spec", "firmware", "updater", "config")
	for _, step := range location {
		if step.element {
			path = path.Index(step.index)
		} else {
			path = path.Child(step.key)
		}
	}
	return path
}

// schemaKeywordTokens returns the JSON pointer tokens of the schema location
// in an absolute keyword location.
func schemaKeywordTokens(absoluteKeywordLocation string) []string {
	_, fragment, found := strings.Cut(absoluteKeywordLocation, "#")
	fragment = strings.TrimPrefix(fragment, "/")
	if !found || fragment == "" {
		return nil
	}
	tokens := strings.Split(fragment, "/")
	for i, token := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens
}

func isArrayIndexToken(token string) bool {
	_, err := strconv.Atoi(token)
	return err == nil
}

func containsFirmwareTemplate(value string) bool {
	return strings.Contains(value, "{{")
}

func decodeFirmwareConfig(config map[string]any) (any, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return decodeJSONWithNumbers(raw)
}

func decodeJSONWithNumbers(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func sortedNames(names map[string]struct{}) []string {
	if len(names) == 0 {
		return nil
	}
	sorted := lo.Keys(names)
	slices.Sort(sorted)
	return sorted
}

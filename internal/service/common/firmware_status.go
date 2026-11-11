package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const firmwareObservationSchemaURL = "file:///firmware-observation.json"

// ValidateFirmwareStatus validates reported firmware status against the
// retained schema evidence of the updater releases that produced it.
//
// Each observation is validated against the observation schema that its own
// backend.updater and backend.schemaDigest identify. That release may differ
// from the desired updater and from the release of the current attempt, and an
// observation needs no attempt. A reported exceeded budget must be a budget
// key of the release that the attempt pinned; without a pinned release no
// budget applies, so none may be reported.
//
// Only retained schema evidence is looked up. It stays available after a
// release loses its authorization, so validating status neither needs nor
// grants current execution authorization. Retained schemas are compiled as
// supplied, with formats asserted and no external documents loaded. Evidence
// that is unavailable, unknown or inconsistent fails closed. The status is not
// modified.
func ValidateFirmwareStatus(ctx context.Context, provider ApprovedDescriptorProvider, status *domain.FirmwareStatus) domain.Status {
	if status == nil {
		return domain.StatusOK()
	}
	check := &firmwareStatusCheck{ctx: ctx, provider: provider, retained: map[string]*retainedFirmwareSchema{}}
	path := field.NewPath("status", "firmware")
	for i := range status.Components {
		observation := status.Components[i].Observation
		if observation == nil || observation.Backend == nil {
			continue
		}
		if failure := check.validateObservation(observation.Backend, path.Child("components").Index(i).Child("observation", "backend")); failure != nil {
			return *failure
		}
	}
	if failure := check.validateExceededBudgets(status, path); failure != nil {
		return *failure
	}
	if len(check.errs) > 0 {
		return domain.StatusBadRequest(errors.Join(check.errs...).Error())
	}
	return domain.StatusOK()
}

// firmwareStatusCheck accumulates the field errors of one status validation
// and caches the retained evidence it looked up.
type firmwareStatusCheck struct {
	ctx      context.Context
	provider ApprovedDescriptorProvider
	retained map[string]*retainedFirmwareSchema
	errs     []error
}

// retainedFirmwareSchema is the outcome of looking up the retained evidence
// of one updater and schema digest.
type retainedFirmwareSchema struct {
	evidence    *RetainedSchemaEvidence
	unavailable bool
	schema      *jsonschema.Schema
}

func (c *firmwareStatusCheck) validateObservation(backend *domain.FirmwareObservationBackend, path *field.Path) *domain.Status {
	if !c.requireIdentity(backend.Updater, backend.SchemaDigest, path.Child("updater"), path.Child("schemaDigest")) {
		return nil
	}
	retained, failure := c.lookup(backend.Updater, backend.SchemaDigest)
	if failure != nil {
		return failure
	}
	if !c.usable(retained, backend.Updater, backend.SchemaDigest, path, path.Child("schemaDigest")) {
		return nil
	}
	if retained.schema == nil {
		schema, err := compileObservationSchema(retained.evidence.ObservationSchema)
		if err != nil {
			failure := domain.StatusInternalServerError(fmt.Sprintf("retained observation schema %s of updater %s is not usable: %v", backend.SchemaDigest, backend.Updater, err))
			return &failure
		}
		retained.schema = schema
	}

	dataPath := path.Child("data")
	data, err := decodeFirmwareConfig(backend.Data)
	if err != nil {
		failure := domain.StatusInternalServerError(fmt.Sprintf("failed to decode %s: %v", dataPath, err))
		return &failure
	}
	err = retained.schema.Validate(data)
	if err == nil {
		return nil
	}
	var root *jsonschema.ValidationError
	if !errors.As(err, &root) {
		failure := domain.StatusInternalServerError(fmt.Sprintf("failed to validate %s: %v", dataPath, err))
		return &failure
	}
	var failures []*jsonschema.ValidationError
	collectSchemaFailures(root, true, &failures)
	for _, f := range failures {
		c.errs = append(c.errs, fmt.Errorf("%s: %s", instanceFieldPath(dataPath, data, f.InstanceLocation), f.Message))
	}
	return nil
}

// validateExceededBudgets checks every reported exceeded budget against the
// budget keys of the release that the attempt pinned.
func (c *firmwareStatusCheck) validateExceededBudgets(status *domain.FirmwareStatus, path *field.Path) *domain.Status {
	type reported struct {
		path *field.Path
		key  string
	}
	var budgets []reported
	if status.Attempt != nil && status.Attempt.Error != nil && status.Attempt.Error.ExceededBudget != nil {
		budgets = append(budgets, reported{path.Child("attempt", "error", "exceededBudget"), *status.Attempt.Error.ExceededBudget})
	}
	for i, component := range status.Components {
		if component.Error != nil && component.Error.ExceededBudget != nil {
			budgets = append(budgets, reported{path.Child("components").Index(i).Child("error", "exceededBudget"), *component.Error.ExceededBudget})
		}
	}
	if len(budgets) == 0 {
		return nil
	}

	if status.Attempt == nil || status.Attempt.Updater == nil {
		for _, budget := range budgets {
			c.errs = append(c.errs, field.Forbidden(budget.path, "only an attempt with a pinned updater can report an exceeded budget"))
		}
		return nil
	}
	pinned := status.Attempt.Updater
	pinnedPath := path.Child("attempt", "updater")
	if !c.requireIdentity(pinned.Name, pinned.SchemaDigest, pinnedPath.Child("name"), pinnedPath.Child("schemaDigest")) {
		return nil
	}
	retained, failure := c.lookup(pinned.Name, pinned.SchemaDigest)
	if failure != nil {
		return failure
	}
	if !c.usable(retained, pinned.Name, pinned.SchemaDigest, pinnedPath, pinnedPath.Child("schemaDigest")) {
		return nil
	}
	allowed := map[string]bool{}
	for _, key := range retained.evidence.BudgetKeySet {
		allowed[key] = true
	}
	for _, budget := range budgets {
		if !allowed[budget.key] {
			c.errs = append(c.errs, field.NotSupported(budget.path, budget.key, retained.evidence.BudgetKeySet))
		}
	}
	return nil
}

// requireIdentity reports whether both parts of a retained-evidence identity
// are set, recording a field error for each missing part.
func (c *firmwareStatusCheck) requireIdentity(updater, schemaDigest string, updaterPath, digestPath *field.Path) bool {
	if updater == "" {
		c.errs = append(c.errs, field.Required(updaterPath, ""))
	}
	if schemaDigest == "" {
		c.errs = append(c.errs, field.Required(digestPath, ""))
	}
	return updater != "" && schemaDigest != ""
}

// lookup returns the retained evidence of an updater and schema digest,
// looking it up once per validation. A failed lookup or evidence that does not
// describe the requested digest fails closed with an internal error.
func (c *firmwareStatusCheck) lookup(updater, schemaDigest string) (*retainedFirmwareSchema, *domain.Status) {
	key := updater + "@" + schemaDigest
	if retained, ok := c.retained[key]; ok {
		return retained, nil
	}
	retained := &retainedFirmwareSchema{}
	var evidence *RetainedSchemaEvidence
	var err error
	if c.provider == nil {
		err = ErrApprovedDescriptorsUnavailable
	} else {
		evidence, err = c.provider.LookupSchemaByDigest(c.ctx, updater, schemaDigest)
	}
	switch {
	case errors.Is(err, ErrApprovedDescriptorsUnavailable):
		retained.unavailable = true
	case err != nil:
		failure := domain.StatusInternalServerError(fmt.Sprintf("failed to look up retained schema %s of updater %s: %v", schemaDigest, updater, err))
		return nil, &failure
	case evidence != nil && evidence.SchemaDigest != schemaDigest:
		failure := domain.StatusInternalServerError(fmt.Sprintf("retained schema evidence for digest %s of updater %s describes digest %s", schemaDigest, updater, evidence.SchemaDigest))
		return nil, &failure
	default:
		retained.evidence = evidence
	}
	c.retained[key] = retained
	return retained, nil
}

// usable reports whether retained evidence was found, recording why it
// cannot be used otherwise.
func (c *firmwareStatusCheck) usable(retained *retainedFirmwareSchema, updater, schemaDigest string, path, digestPath *field.Path) bool {
	switch {
	case retained.unavailable:
		c.errs = append(c.errs, fmt.Errorf("%s: retained updater schemas are unavailable, so the firmware status cannot be validated", path))
		return false
	case retained.evidence == nil:
		c.errs = append(c.errs, field.Invalid(digestPath, schemaDigest, fmt.Sprintf("no retained observation schema of updater %s has this digest", updater)))
		return false
	}
	return true
}

func compileObservationSchema(raw []byte) (*jsonschema.Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("the observation schema is missing")
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	if err := compiler.AddResource(firmwareObservationSchemaURL, bytes.NewReader(raw)); err != nil {
		return nil, errors.New("the observation schema is not valid JSON")
	}
	schema, err := compiler.Compile(firmwareObservationSchemaURL)
	if err != nil {
		return nil, errors.New("the observation schema is not a valid JSON Schema")
	}
	return schema, nil
}

// instanceFieldPath returns the field path of the value at a JSON Pointer in
// decoded data below base.
func instanceFieldPath(base *field.Path, data any, pointer string) string {
	location, _, ok := resolveFirmwareInstance(data, pointer)
	if !ok {
		return base.String() + pointer
	}
	path := base
	for _, step := range location {
		if step.element {
			path = path.Index(step.index)
		} else {
			path = path.Child(step.key)
		}
	}
	return path.String()
}

// FirmwareStatusEqual reports whether two firmware statuses have the same
// JSON representation.
func FirmwareStatusEqual(a, b *domain.FirmwareStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

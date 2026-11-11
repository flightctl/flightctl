package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const firmwareObservationSchemaURL = "file:///firmware-observation.json"

// ValidateFirmwareStatus validates reported firmware status against the
// retained schema evidence of the updater releases that produced it, after
// checking its structure, including that only ResourceLimitExceeded errors
// name an exceeded budget.
//
// Each observation is validated against the observation schema that its own
// backend.updater and backend.schemaDigest identify. That release may differ
// from the desired updater and from the release of the current attempt, and an
// observation needs no attempt. A reported exceeded budget must be a budget
// key of the release that the attempt pinned; without a pinned release, a
// component's budget must be a key of the release that produced its own
// observation, and the attempt cannot report one.
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
	if errs := status.Validate(); len(errs) > 0 {
		return domain.StatusBadRequest(errors.Join(errs...).Error())
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
// budget keys of the release that defines it. When the attempt has a pinned
// updater, that release defines the budgets of the attempt and of every
// component. Otherwise an attempt cannot report a budget, and each component
// reports budgets of the release that produced its own observation, so a
// component without an attempt is judged by its own evidence only, never by
// another component's or by the desired release.
func (c *firmwareStatusCheck) validateExceededBudgets(status *domain.FirmwareStatus, path *field.Path) *domain.Status {
	type reported struct {
		path      *field.Path
		key       string
		component int
	}
	var budgets []reported
	if status.Attempt != nil && status.Attempt.Error != nil && status.Attempt.Error.ExceededBudget != nil {
		budgets = append(budgets, reported{path: path.Child("attempt", "error", "exceededBudget"), key: *status.Attempt.Error.ExceededBudget, component: -1})
	}
	for i, component := range status.Components {
		if component.Error != nil && component.Error.ExceededBudget != nil {
			budgets = append(budgets, reported{path: path.Child("components").Index(i).Child("error", "exceededBudget"), key: *component.Error.ExceededBudget, component: i})
		}
	}
	if len(budgets) == 0 {
		return nil
	}

	if status.Attempt != nil && status.Attempt.Updater != nil {
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
		for _, budget := range budgets {
			c.checkBudgetKey(budget.path, budget.key, retained.evidence.BudgetKeySet)
		}
		return nil
	}

	for _, budget := range budgets {
		if budget.component < 0 {
			c.errs = append(c.errs, field.Forbidden(budget.path, "only an attempt with a pinned updater can report an exceeded budget"))
			continue
		}
		var backend *domain.FirmwareObservationBackend
		if observation := status.Components[budget.component].Observation; observation != nil {
			backend = observation.Backend
		}
		if backend == nil || backend.Updater == "" || backend.SchemaDigest == "" {
			c.errs = append(c.errs, field.Forbidden(budget.path, "without an attempt with a pinned updater, the component's own observation must identify the updater release that defines the budget"))
			continue
		}
		retained, failure := c.lookup(backend.Updater, backend.SchemaDigest)
		if failure != nil {
			return failure
		}
		if retained.unavailable || retained.evidence == nil {
			c.errs = append(c.errs, field.Forbidden(budget.path, "the retained schema of the component's own observation is unavailable or unknown, so the budget cannot be validated"))
			continue
		}
		c.checkBudgetKey(budget.path, budget.key, retained.evidence.BudgetKeySet)
	}
	return nil
}

// checkBudgetKey records an error unless key is one of the budget keys.
func (c *firmwareStatusCheck) checkBudgetKey(path *field.Path, key string, budgetKeys []string) {
	if !slices.Contains(budgetKeys, key) {
		c.errs = append(c.errs, field.NotSupported(path, key, budgetKeys))
	}
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

// FirmwareStatusPatchOutcome tells how a JSON Patch request leaves the
// firmware status of a device.
type FirmwareStatusPatchOutcome int

const (
	// FirmwareStatusKept means the patched status holds the firmware status
	// that the request leaves: the stored one when the request does not touch
	// it, or the one the request wrote.
	FirmwareStatusKept FirmwareStatusPatchOutcome = iota
	// FirmwareStatusOmitted means the request replaced an enclosing object
	// with a value that does not mention the firmware status.
	FirmwareStatusOmitted
	// FirmwareStatusRemoved means the request explicitly removed the firmware
	// status, or an object enclosing it, or set it to null.
	FirmwareStatusRemoved
)

const deviceFirmwareStatusPatchPath = "/status/firmware"

// CheckFirmwareAttemptRetained rejects a firmware status that would erase an
// outstanding attempt of the stored status: no firmware status, one without an
// attempt, or one with a different attempt. Progress of the same attempt,
// including to a terminal phase, is accepted, and an attempt that is no longer
// outstanding may be dropped.
func CheckFirmwareAttemptRetained(stored, candidate *domain.FirmwareStatus) error {
	if stored == nil || !domain.FirmwareAttemptOutstanding(stored.Attempt) {
		return nil
	}
	if candidate == nil || candidate.Attempt == nil || candidate.Attempt.Id != stored.Attempt.Id {
		return fmt.Errorf("%s: cannot erase an outstanding attempt", field.NewPath("status", "firmware", "attempt"))
	}
	return nil
}

// ClassifyFirmwareStatusPatch tells how a JSON Patch request leaves the
// firmware status of a device, deciding from the raw patched document because
// decoding turns a removed, a null and an omitted firmware status alike into
// an absent one. A firmware status that the request writes is checked against
// its API schema while still raw JSON, so a key that differs from a field only
// in case is rejected instead of dropped; observation data stays open for the
// retained observation schema.
func ClassifyFirmwareStatusPatch[T any](current T, patch domain.PatchRequest) (FirmwareStatusPatchOutcome, error) {
	if !patchWritesPath(patch, deviceFirmwareStatusPatchPath) {
		return FirmwareStatusKept, nil
	}
	patched, err := jsonpatch.ApplyToJSON(current, patch)
	if err != nil {
		return 0, err
	}
	var document any
	if err := json.Unmarshal(patched, &document); err != nil {
		return 0, fmt.Errorf("patch produced an invalid resource: %w", err)
	}
	value, found := jsonPointerValue(document, deviceFirmwareStatusPatchPath)
	switch {
	case found && value != nil:
		if err := validateRawFirmware(value, deviceFirmwareStatusPatchPath, firmwareStatusSchemaName); err != nil {
			return 0, err
		}
		return FirmwareStatusKept, nil
	case found:
		return FirmwareStatusRemoved, nil
	}
	for _, operation := range patch {
		if operation.Op == "remove" && (operation.Path == deviceFirmwareStatusPatchPath || operation.Path == "/status") {
			return FirmwareStatusRemoved, nil
		}
	}
	return FirmwareStatusOmitted, nil
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

package labelsyncmapping

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util/validation"
)

const (
	maxCachedPrograms = 256
	maxExpressionCost = 1_000
	maxMapEntries     = 50
)

type FailureKind string

const (
	FailureComplexValue      FailureKind = "ComplexValue"
	FailureEvaluation        FailureKind = "Evaluation"
	FailureInvalidActivation FailureKind = "InvalidActivation"
	FailureInvalidExpression FailureKind = "InvalidExpression"
	FailureInvalidMapEntry   FailureKind = "InvalidMapEntry"
	FailureCardinality       FailureKind = "Cardinality"
	FailureSanitization      FailureKind = "Sanitization"
)

// Result is a scalar value, map, or successful evaluation with no value.
type Result interface {
	isResult()
}

// ResultKind identifies the expected shape of a mapping expression result.
type ResultKind string

const (
	// ResultKindScalar expects a scalar expression result.
	ResultKindScalar ResultKind = "scalar"
	// ResultKindMap expects a map expression result.
	ResultKindMap ResultKind = "map"
)

// ScalarResult is a sanitized scalar value produced by an expression.
type ScalarResult string

func (ScalarResult) isResult() {}

// MapResult contains sanitized values returned by a map expression.
type MapResult map[string]string

func (MapResult) isResult() {}

// NoResult represents a successful expression with no value to map.
type NoResult struct{}

func (NoResult) isResult() {}

type EntryFailure struct {
	Key     string
	Kind    FailureKind
	Message string
}

type EvaluationError struct {
	Kind          FailureKind
	EntryFailures []EntryFailure
	err           error
}

func (e *EvaluationError) Error() string {
	return e.err.Error()
}

func (e *EvaluationError) Unwrap() error {
	return e.err
}

// Activation is a value passed to CEL when evaluating an expression.
type Activation interface{}

type Evaluator interface {
	ValidateExpressionIs(expression string, expectedKind ResultKind) error
	// Evaluate evaluates an expression using a prebuilt activation.
	Evaluate(expression string, activation Activation) (Result, error)
}

type evaluator struct {
	env      *cel.Env
	mu       sync.Mutex
	programs map[string]*list.Element
	lru      *list.List
}

type cachedProgram struct {
	expression string
	program    cel.Program
	outputType *cel.Type
	err        error
}

func NewEvaluator() (Evaluator, error) {
	env, err := cel.NewEnv(
		cel.Variable("metadata", cel.DynType),
		cel.Variable("spec", cel.DynType),
		cel.Variable("status", cel.DynType),
		cel.OptionalTypes(),
		ext.TwoVarComprehensions(),
		semverLibrary(),
		cel.ParserExpressionSizeLimit(maxExpressionCost),
	)
	if err != nil {
		return nil, fmt.Errorf("creating CEL environment: %w", err)
	}

	return &evaluator{
		env:      env,
		programs: make(map[string]*list.Element, maxCachedPrograms),
		lru:      list.New(),
	}, nil
}

func (e *evaluator) Evaluate(expression string, activation Activation) (Result, error) {
	if activation == nil {
		return nil, evaluationError(FailureInvalidActivation, "using CEL activation", fmt.Errorf("activation is nil"))
	}

	program, outputType, err := e.program(expression)
	if err != nil {
		return nil, err
	}
	if !validOutputType(outputType) {
		return nil, evaluationError(FailureInvalidExpression, "checking CEL output shape", fmt.Errorf("expression type %q is not a supported scalar or map result", outputType))
	}
	value, _, err := program.Eval(activation)
	if err != nil {
		if isMissingError(err) {
			return NoResult{}, nil
		}
		return nil, evaluationError(FailureEvaluation, "evaluating CEL expression", err)
	}
	if optional, ok := value.(*types.Optional); ok {
		if !optional.HasValue() {
			return NoResult{}, nil
		}
		value = optional.GetValue()
	}

	if types.IsError(value) {
		if valueError, ok := value.(error); ok && isMissingError(valueError) {
			return NoResult{}, nil
		}
		return nil, evaluationError(FailureEvaluation, "evaluating CEL expression", fmt.Errorf("%v", value))
	}

	if _, isNull := value.(types.Null); isNull {
		return NoResult{}, nil
	}
	if value.Type().TypeName() == "map" {
		return mapResult(value)
	}

	return scalarResult(value)
}

func (e *evaluator) ValidateExpressionIs(expression string, expectedKind ResultKind) error {
	_, outputType, err := e.program(expression)
	if err != nil {
		return err
	}
	if !validOutputType(outputType) {
		return evaluationError(FailureInvalidExpression, "checking CEL output shape", fmt.Errorf("expression type %q is not a supported scalar or map result", outputType))
	}
	if expectedKind != ResultKindScalar && expectedKind != ResultKindMap {
		return evaluationError(FailureInvalidExpression, "checking CEL expected result kind", fmt.Errorf("unsupported expected result kind %q", expectedKind))
	}
	if actualKind, isKnown := knownOutputKind(outputType); isKnown && actualKind != expectedKind {
		return evaluationError(FailureInvalidExpression, "checking CEL expected result kind", fmt.Errorf("expression type %q produces %s, expected %s", outputType, actualKind, expectedKind))
	}
	return nil
}

func (e *evaluator) program(expression string) (cel.Program, *cel.Type, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if element, ok := e.programs[expression]; ok {
		e.lru.MoveToFront(element)
		cached := element.Value.(*cachedProgram)
		return cached.program, cached.outputType, cached.err
	}

	program, outputType, err := e.compile(expression)
	element := e.lru.PushFront(&cachedProgram{expression: expression, program: program, outputType: outputType, err: err})
	e.programs[expression] = element
	if e.lru.Len() > maxCachedPrograms {
		oldest := e.lru.Back()
		cached := oldest.Value.(*cachedProgram)
		delete(e.programs, cached.expression)
		e.lru.Remove(oldest)
	}
	return program, outputType, err
}

func (e *evaluator) compile(expression string) (cel.Program, *cel.Type, error) {
	parsed, issues := e.env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, nil, evaluationError(FailureInvalidExpression, "parsing CEL expression", issues.Err())
	}

	checked, issues := e.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		failureKind := FailureInvalidExpression
		if strings.Contains(issues.Err().Error(), "undeclared reference") {
			failureKind = FailureInvalidActivation
		}
		return nil, nil, evaluationError(failureKind, "checking CEL expression", issues.Err())
	}

	program, err := e.env.Program(checked, cel.CostLimit(maxExpressionCost))
	if err != nil {
		return nil, nil, evaluationError(FailureInvalidExpression, "building CEL program", err)
	}
	return program, checked.OutputType(), nil
}

func validOutputType(output *cel.Type) bool {
	if output == nil {
		return true
	}

	if output.Kind() == cel.OpaqueKind && output.TypeName() == "optional_type" {
		parameters := output.Parameters()
		return len(parameters) == 0 || validOptionalOutputType(parameters[0])
	}

	if output.Kind() == cel.DynKind || output.Kind() == cel.NullTypeKind || output.Kind() == cel.TypeParamKind {
		return true
	}
	if scalarType(output) {
		return true
	}
	if output.Kind() != cel.MapKind {
		return false
	}
	parameters := output.Parameters()
	return len(parameters) == 2 &&
		(parameters[0].Kind() == cel.StringKind || indeterminateType(parameters[0])) &&
		mapScalarType(parameters[1])
}

func validOptionalOutputType(output *cel.Type) bool {
	if indeterminateType(output) {
		return true
	}
	return validOutputType(output)
}

func knownOutputKind(output *cel.Type) (ResultKind, bool) {
	if output == nil || output.Kind() == cel.DynKind || output.Kind() == cel.NullTypeKind || output.Kind() == cel.TypeParamKind {
		return "", false
	}
	if output.Kind() == cel.OpaqueKind && output.TypeName() == "optional_type" {
		parameters := output.Parameters()
		if len(parameters) == 0 {
			return "", false
		}
		return knownOutputKind(parameters[0])
	}
	if scalarType(output) {
		return ResultKindScalar, true
	}
	if output.Kind() == cel.MapKind {
		return ResultKindMap, true
	}
	return "", false
}

func mapScalarType(valueType *cel.Type) bool {
	return valueType != nil && (indeterminateType(valueType) || valueType.Kind() == cel.NullTypeKind || scalarType(valueType))
}

func scalarType(valueType *cel.Type) bool {
	if valueType == nil {
		return false
	}
	switch valueType.Kind() {
	case cel.BoolKind, cel.DoubleKind, cel.IntKind, cel.StringKind, cel.UintKind:
		return true
	default:
		return false
	}
}

func indeterminateType(valueType *cel.Type) bool {
	return valueType != nil && (valueType.Kind() == cel.DynKind || valueType.Kind() == cel.TypeParamKind)
}

func scalarResult(value ref.Val) (Result, error) {
	raw, err := scalarString(value)
	if err != nil {
		return nil, evaluationError(FailureComplexValue, "converting CEL scalar result", err)
	}
	if raw == "" {
		return NoResult{}, nil
	}

	sanitized := validation.SanitizeLabelValue(raw)
	if sanitized == "" {
		return nil, evaluationError(FailureSanitization, "sanitizing CEL scalar result", fmt.Errorf("scalar result %q sanitizes to an empty label value", raw))
	}
	return ScalarResult(sanitized), nil
}

func scalarString(value ref.Val) (string, error) {
	var raw string
	switch value := value.(type) {
	case types.String:
		raw = string(value)
	case types.Bool:
		raw = strconv.FormatBool(bool(value))
	case types.Int:
		raw = strconv.FormatInt(int64(value), 10)
	case types.Uint:
		raw = strconv.FormatUint(uint64(value), 10)
	case types.Double:
		raw = strconv.FormatFloat(float64(value), 'g', -1, 64)
	default:
		return "", fmt.Errorf("unsupported CEL scalar type %q", value.Type().TypeName())
	}
	return raw, nil
}

func mapResult(value ref.Val) (Result, error) {
	mapper, ok := value.(traits.Mapper)
	if !ok {
		return nil, evaluationError(FailureComplexValue, "converting CEL map result", fmt.Errorf("expected map result, got %q", value.Type().TypeName()))
	}
	sizeValue := mapper.Size()
	if types.IsError(sizeValue) {
		return nil, evaluationError(FailureEvaluation, "checking CEL map result size", fmt.Errorf("%v", sizeValue))
	}
	size, ok := sizeValue.(types.Int)
	if !ok {
		return nil, evaluationError(FailureEvaluation, "checking CEL map result size", fmt.Errorf("unexpected map size type %q", sizeValue.Type().TypeName()))
	}
	if int64(size) > maxMapEntries {
		return nil, evaluationError(FailureCardinality, "converting CEL map result", fmt.Errorf("map result has %d entries; limit is %d", size, maxMapEntries))
	}

	result := make(MapResult, int(size))
	var entryFailures []EntryFailure
	iterator := mapper.Iterator()
	for {
		hasNext := iterator.HasNext()
		if types.IsError(hasNext) {
			return nil, evaluationError(FailureEvaluation, "iterating CEL map result", fmt.Errorf("%v", hasNext))
		}
		more, ok := hasNext.(types.Bool)
		if !ok {
			return nil, evaluationError(FailureEvaluation, "iterating CEL map result", fmt.Errorf("unexpected iterator state %q", hasNext.Type().TypeName()))
		}
		if !bool(more) {
			break
		}
		keyValue := iterator.Next()
		if types.IsError(keyValue) {
			return nil, evaluationError(FailureEvaluation, "iterating CEL map result", fmt.Errorf("%v", keyValue))
		}
		keyLabel := fmt.Sprintf("%v", keyValue)
		entryValue, found := mapper.Find(keyValue)
		if !found {
			entryFailures = append(entryFailures, EntryFailure{Key: keyLabel, Kind: FailureInvalidMapEntry, Message: "map entry could not be read"})
			continue
		}
		if types.IsError(entryValue) {
			entryFailures = append(entryFailures, EntryFailure{Key: keyLabel, Kind: FailureInvalidMapEntry, Message: fmt.Sprintf("reading map entry: %v", entryValue)})
			continue
		}
		key, isString := keyValue.(types.String)
		if !isString {
			entryFailures = append(entryFailures, EntryFailure{Key: keyLabel, Kind: FailureInvalidMapEntry, Message: fmt.Sprintf("map key has unsupported type %q", keyValue.Type().TypeName())})
			continue
		}
		if keyErrors := validation.ValidateLabelKey(string(key)); len(keyErrors) > 0 {
			entryFailures = append(entryFailures, EntryFailure{Key: string(key), Kind: FailureInvalidMapEntry, Message: strings.Join(keyErrors, "; ")})
			continue
		}
		if _, isNull := entryValue.(types.Null); isNull {
			continue
		}

		raw, err := scalarString(entryValue)
		if err != nil {
			entryFailures = append(entryFailures, EntryFailure{Key: string(key), Kind: FailureInvalidMapEntry, Message: err.Error()})
			continue
		}
		if raw == "" {
			continue
		}
		sanitized := validation.SanitizeLabelValue(raw)
		if sanitized == "" {
			entryFailures = append(entryFailures, EntryFailure{Key: string(key), Kind: FailureSanitization, Message: fmt.Sprintf("map value %q sanitizes to an empty label value", raw)})
			continue
		}
		result[string(key)] = sanitized
	}
	if len(entryFailures) > 0 {
		sort.Slice(entryFailures, func(i, j int) bool {
			if entryFailures[i].Key != entryFailures[j].Key {
				return entryFailures[i].Key < entryFailures[j].Key
			}
			return entryFailures[i].Kind < entryFailures[j].Kind
		})
		return nil, entryFailuresEvaluationError(FailureInvalidMapEntry, "converting CEL map result", entryFailures)
	}
	return result, nil
}

func entryFailuresEvaluationError(kind FailureKind, operation string, failures []EntryFailure) *EvaluationError {
	return &EvaluationError{
		Kind:          kind,
		EntryFailures: failures,
		err:           fmt.Errorf("%s: %s", operation, formatEntryFailures(failures)),
	}
}

func formatEntryFailures(failures []EntryFailure) string {
	details := make([]string, 0, len(failures))
	for _, failure := range failures {
		details = append(details, fmt.Sprintf("%q: %s", failure.Key, failure.Message))
	}
	return strings.Join(details, "; ")
}

// ActivateDevice builds a CEL activation from the supported device roots.
func ActivateDevice(device domain.Device) (Activation, error) {
	type deviceRoots struct {
		Metadata domain.ObjectMeta    `json:"metadata"`
		Spec     *domain.DeviceSpec   `json:"spec,omitempty"`
		Status   *domain.DeviceStatus `json:"status,omitempty"`
	}
	type decodedRoots struct {
		Metadata any `json:"metadata"`
		Spec     any `json:"spec"`
		Status   any `json:"status"`
	}

	data, err := json.Marshal(deviceRoots{
		Metadata: device.Metadata,
		Spec:     device.Spec,
		Status:   device.Status,
	})
	if err != nil {
		return nil, evaluationError(FailureInvalidActivation, "building CEL activation", fmt.Errorf("marshaling device roots: %w", err))
	}

	var roots decodedRoots
	if err := json.Unmarshal(data, &roots); err != nil {
		return nil, evaluationError(FailureInvalidActivation, "building CEL activation", fmt.Errorf("unmarshaling device roots: %w", err))
	}
	return map[string]any{
		"metadata": roots.Metadata,
		"spec":     roots.Spec,
		"status":   roots.Status,
	}, nil
}

func evaluationError(kind FailureKind, operation string, err error) error {
	return &EvaluationError{
		Kind: kind,
		err:  fmt.Errorf("%s: %w", operation, err),
	}
}

func isMissingError(err error) bool {
	return strings.Contains(err.Error(), "no such key")
}

package labelsyncmapping

import (
	"container/list"
	"encoding/json"
	"errors"
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
		return nil, err
	}

	return &evaluator{
		env:      env,
		programs: make(map[string]*list.Element, maxCachedPrograms),
		lru:      list.New(),
	}, nil
}

func (e *evaluator) Evaluate(expression string, activation Activation) (Result, error) {
	if activation == nil {
		return nil, errors.New("activation is nil")
	}

	program, outputType, err := e.program(expression)
	if err != nil {
		return nil, err
	}
	if !validOutputType(outputType) {
		return nil, fmt.Errorf("expression type %q is not a supported scalar or map result", outputType)
	}
	value, _, err := program.Eval(activation)
	if err != nil {
		if isMissingError(err) {
			return NoResult{}, nil
		}
		return nil, fmt.Errorf("evaluating CEL expression: %w", err)
	}
	if optional, ok := value.(*types.Optional); ok {
		if !optional.HasValue() {
			return NoResult{}, nil
		}
		value = optional.GetValue()
	}

	if types.IsError(value) {
		if valueError, ok := value.(error); ok {
			if isMissingError(valueError) {
				return NoResult{}, nil
			}
			return nil, fmt.Errorf("evaluating CEL expression: %w", valueError)
		}
		return nil, fmt.Errorf("%v", value)
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
		return fmt.Errorf("expression type %q is not a supported scalar or map result", outputType)
	}
	if expectedKind != ResultKindScalar && expectedKind != ResultKindMap {
		return fmt.Errorf("unsupported expected result kind %q", expectedKind)
	}
	if actualKind, isKnown := knownOutputKind(outputType); isKnown && actualKind != expectedKind {
		return fmt.Errorf("expression type %q produces %s, expected %s", outputType, actualKind, expectedKind)
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
		return nil, nil, fmt.Errorf("parsing CEL expression: %w", issues.Err())
	}

	checked, issues := e.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, nil, fmt.Errorf("checking CEL expression: %w", issues.Err())
	}

	program, err := e.env.Program(checked, cel.CostLimit(maxExpressionCost))
	if err != nil {
		return nil, nil, fmt.Errorf("building CEL program: %w", err)
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
		return nil, err
	}
	if raw == "" {
		return NoResult{}, nil
	}

	sanitized := validation.SanitizeLabelValue(raw)
	if sanitized == "" {
		return nil, fmt.Errorf("scalar result %q sanitizes to an empty label value", raw)
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
		return nil, fmt.Errorf("expected map result, got %q", value.Type().TypeName())
	}
	sizeValue := mapper.Size()
	if types.IsError(sizeValue) {
		return nil, fmt.Errorf("%v", sizeValue)
	}
	size, ok := sizeValue.(types.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected map size type %q", sizeValue.Type().TypeName())
	}
	if int64(size) > maxMapEntries {
		return nil, fmt.Errorf("map result has %d entries; limit is %d", size, maxMapEntries)
	}

	result := make(MapResult, int(size))
	var entryErrors []string
	addEntryError := func(key, message string) {
		entryErrors = append(entryErrors, fmt.Sprintf("%q: %s", key, message))
	}
	iterator := mapper.Iterator()
	for {
		hasNext := iterator.HasNext()
		if types.IsError(hasNext) {
			return nil, fmt.Errorf("%v", hasNext)
		}
		more, ok := hasNext.(types.Bool)
		if !ok {
			return nil, fmt.Errorf("unexpected iterator state %q", hasNext.Type().TypeName())
		}
		if !bool(more) {
			break
		}
		keyValue := iterator.Next()
		if types.IsError(keyValue) {
			return nil, fmt.Errorf("%v", keyValue)
		}
		keyLabel := fmt.Sprintf("%v", keyValue)
		entryValue, found := mapper.Find(keyValue)
		if !found {
			addEntryError(keyLabel, "map entry could not be read")
			continue
		}
		if types.IsError(entryValue) {
			addEntryError(keyLabel, fmt.Sprintf("reading map entry: %v", entryValue))
			continue
		}
		key, isString := keyValue.(types.String)
		if !isString {
			addEntryError(keyLabel, fmt.Sprintf("map key has unsupported type %q", keyValue.Type().TypeName()))
			continue
		}
		if keyErrors := validation.ValidateLabelKey(string(key)); len(keyErrors) > 0 {
			addEntryError(string(key), strings.Join(keyErrors, "; "))
			continue
		}
		if _, isNull := entryValue.(types.Null); isNull {
			continue
		}

		raw, err := scalarString(entryValue)
		if err != nil {
			addEntryError(string(key), err.Error())
			continue
		}
		if raw == "" {
			continue
		}
		sanitized := validation.SanitizeLabelValue(raw)
		if sanitized == "" {
			addEntryError(string(key), fmt.Sprintf("map value %q sanitizes to an empty label value", raw))
			continue
		}
		result[string(key)] = sanitized
	}
	if len(entryErrors) > 0 {
		sort.Strings(entryErrors)
		return nil, errors.New(strings.Join(entryErrors, "; "))
	}
	return result, nil
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
		return nil, fmt.Errorf("marshaling device roots: %w", err)
	}

	var roots decodedRoots
	if err := json.Unmarshal(data, &roots); err != nil {
		return nil, fmt.Errorf("unmarshaling device roots: %w", err)
	}
	return map[string]any{
		"metadata": roots.Metadata,
		"spec":     roots.Spec,
		"status":   roots.Status,
	}, nil
}

func isMissingError(err error) bool {
	return strings.Contains(err.Error(), "no such key")
}

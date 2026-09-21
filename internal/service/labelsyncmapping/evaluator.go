package labelsyncmapping

import (
	"container/list"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

const (
	maxCachedPrograms = 256
	maxExpressionCost = 1_000
)

type FailureKind string

const (
	FailureComplexValue      FailureKind = "ComplexValue"
	FailureEvaluation        FailureKind = "Evaluation"
	FailureInvalidActivation FailureKind = "InvalidActivation"
	FailureInvalidExpression FailureKind = "InvalidExpression"
	FailureSanitization      FailureKind = "Sanitization"
)

type Result struct {
	Present bool
	Value   string
}

type EvaluationError struct {
	Kind FailureKind
	err  error
}

func (e *EvaluationError) Error() string {
	return e.err.Error()
}

func (e *EvaluationError) Unwrap() error {
	return e.err
}

type Evaluator interface {
	Evaluate(expression string, device v1beta1.Device) (Result, error)
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
	err        error
}

func NewEvaluator() (Evaluator, error) {
	env, err := cel.NewEnv(
		cel.Variable("metadata", cel.DynType),
		cel.Variable("spec", cel.DynType),
		cel.Variable("status", cel.DynType),
		cel.OptionalTypes(),
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

func (e *evaluator) Evaluate(expression string, device v1beta1.Device) (Result, error) {
	program, err := e.program(expression)
	if err != nil {
		return Result{}, err
	}

	activation, err := activation(device)
	if err != nil {
		return Result{}, evaluationError(FailureInvalidActivation, "building CEL activation", err)
	}

	value, _, err := program.Eval(activation)
	if err != nil {
		if isMissingError(err) {
			return Result{}, nil
		}
		return Result{}, evaluationError(FailureEvaluation, "evaluating CEL expression", err)
	}
	if optional, ok := value.(*types.Optional); ok {
		if !optional.HasValue() {
			return Result{}, nil
		}
		value = optional.GetValue()
	}

	if types.IsError(value) {
		if valueError, ok := value.(error); ok && isMissingError(valueError) {
			return Result{}, nil
		}
		return Result{}, evaluationError(FailureEvaluation, "evaluating CEL expression", fmt.Errorf("%v", value))
	}

	return scalarResult(value)
}

func (e *evaluator) program(expression string) (cel.Program, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if element, ok := e.programs[expression]; ok {
		e.lru.MoveToFront(element)
		cached := element.Value.(*cachedProgram)
		return cached.program, cached.err
	}

	program, err := e.compile(expression)
	element := e.lru.PushFront(&cachedProgram{expression: expression, program: program, err: err})
	e.programs[expression] = element
	if e.lru.Len() > maxCachedPrograms {
		oldest := e.lru.Back()
		cached := oldest.Value.(*cachedProgram)
		delete(e.programs, cached.expression)
		e.lru.Remove(oldest)
	}
	return program, err
}

func (e *evaluator) compile(expression string) (cel.Program, error) {
	parsed, issues := e.env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, evaluationError(FailureInvalidExpression, "parsing CEL expression", issues.Err())
	}

	checked, issues := e.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		failureKind := FailureInvalidExpression
		if strings.Contains(issues.Err().Error(), "undeclared reference") {
			failureKind = FailureInvalidActivation
		}
		return nil, evaluationError(failureKind, "checking CEL expression", issues.Err())
	}

	program, err := e.env.Program(checked, cel.CostLimit(maxExpressionCost))
	if err != nil {
		return nil, evaluationError(FailureInvalidExpression, "building CEL program", err)
	}
	return program, nil
}

func scalarResult(value ref.Val) (Result, error) {
	var raw string
	switch value := value.(type) {
	case types.Null:
		return Result{}, nil
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
		return Result{}, evaluationError(FailureComplexValue, "converting CEL result", fmt.Errorf("unsupported CEL result type %q", value.Type().TypeName()))
	}

	sanitized := validation.SanitizeLabelValue(raw)
	if sanitized == "" {
		return Result{}, evaluationError(FailureSanitization, "sanitizing CEL result", fmt.Errorf("scalar result %q sanitizes to an empty label value", raw))
	}
	return Result{Present: true, Value: sanitized}, nil
}

func activation(device v1beta1.Device) (map[string]any, error) {
	type deviceRoots struct {
		Metadata v1beta1.ObjectMeta    `json:"metadata"`
		Spec     *v1beta1.DeviceSpec   `json:"spec,omitempty"`
		Status   *v1beta1.DeviceStatus `json:"status,omitempty"`
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

func evaluationError(kind FailureKind, operation string, err error) error {
	return &EvaluationError{
		Kind: kind,
		err:  fmt.Errorf("%s: %w", operation, err),
	}
}

func isMissingError(err error) bool {
	return strings.Contains(err.Error(), "no such key")
}

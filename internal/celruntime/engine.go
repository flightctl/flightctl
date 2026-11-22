// Package celruntime compiles and evaluates CEL independently of consumer domains.
package celruntime

import (
	"encoding/json"
	"errors"
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"github.com/jellydator/ttlcache/v3"
)

// MaxExpressionSize is the maximum expression size accepted by the CEL parser.
const MaxExpressionSize = 1_000

// MaxExpressionCost bounds the runtime cost of a single evaluation.
const MaxExpressionCost = 10_000

// MaxCachedPrograms bounds cached compilations, including failures, per engine.
const MaxCachedPrograms = 256

// Engine owns a CEL environment. Consumers supply their own declarations and libraries.
type Engine struct {
	env      *cel.Env
	programs *ttlcache.Cache[string, cachedProgram]
}

type cachedProgram struct {
	program *Program
	err     error
}

// Program is a checked expression with its declared output type.
type Program struct {
	program    cel.Program
	outputType *cel.Type
}

// New constructs an engine with the consumer's environment options.
func New(options ...cel.EnvOption) (*Engine, error) {
	options = append(options, cel.ParserExpressionSizeLimit(MaxExpressionSize))
	env, err := cel.NewEnv(options...)
	if err != nil {
		return nil, err
	}
	return &Engine{
		env: env,
		programs: ttlcache.New(
			ttlcache.WithCapacity[string, cachedProgram](MaxCachedPrograms),
			ttlcache.WithTTL[string, cachedProgram](ttlcache.NoTTL),
		),
	}, nil
}

// Compile parses, type-checks, and builds an expression for this environment.
// Successful compilations return the same Program while cached. Eviction does
// not invalidate a Program already held by a caller. Compilation errors are cached too.
// Compile and Program.Evaluate are safe to call concurrently.
func (e *Engine) Compile(expression string) (*Program, error) {
	if item := e.programs.Get(expression); item != nil {
		cached := item.Value()
		return cached.program, cached.err
	}

	program, err := e.compile(expression)
	item, _ := e.programs.GetOrSet(expression, cachedProgram{program: program, err: err})
	cached := item.Value()
	return cached.program, cached.err
}

func (e *Engine) compile(expression string) (*Program, error) {
	parsed, issues := e.env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("parsing CEL expression: %w", issues.Err())
	}
	checked, issues := e.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("checking CEL expression: %w", issues.Err())
	}
	program, err := e.env.Program(checked, cel.CostLimit(MaxExpressionCost))
	if err != nil {
		return nil, fmt.Errorf("building CEL program: %w", err)
	}
	return &Program{program: program, outputType: checked.OutputType()}, nil
}

// OutputType returns the checked CEL result type for consumer-specific validation.
func (p *Program) OutputType() *cel.Type {
	return p.outputType
}

// Evaluate returns the CEL value without consumer-specific conversion.
func (p *Program) Evaluate(activation any) (ref.Val, error) {
	if activation == nil {
		return nil, errors.New("activation is nil")
	}
	value, _, err := p.program.Eval(activation)
	if err != nil {
		return nil, fmt.Errorf("evaluating CEL expression: %w", err)
	}
	return value, nil
}

// Activate normalizes explicitly selected roots through JSON. Consumers choose
// the exposed roots; no device or enrollment fields are added by the runtime.
func Activate(roots map[string]any) (map[string]any, error) {
	data, err := json.Marshal(roots)
	if err != nil {
		return nil, fmt.Errorf("marshaling activation roots: %w", err)
	}
	var activation map[string]any
	if err := json.Unmarshal(data, &activation); err != nil {
		return nil, fmt.Errorf("unmarshaling activation roots: %w", err)
	}
	return activation, nil
}

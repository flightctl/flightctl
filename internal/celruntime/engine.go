// Package celruntime compiles and evaluates CEL independently of consumer domains.
package celruntime

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types/ref"
	"github.com/jellydator/ttlcache/v3"
)

// Config overrides the defaults for one engine. Zero values use the defaults.
type Config struct {
	MaxExpressionSize  int           // Maximum number of Unicode code points per expression.
	MaxExpressionNodes int           // Maximum parsed AST nodes before type-checking. Zero disables this limit.
	MaxExpressionCost  uint64        // Maximum CEL cost units per evaluation.
	CacheCapacity      uint64        // Maximum cached compilations, including failures.
	CacheTTL           time.Duration // Maximum idle time for cached compilations. Zero means no expiration.
}

// Option configures an engine before its CEL environment is created.
type Option func(*engineOptions)

type engineOptions struct {
	config     Config
	envOptions []cel.EnvOption
}

// WithConfig supplies the runtime limits and cache settings for an engine.
func WithConfig(config Config) Option {
	return func(settings *engineOptions) {
		settings.config = config
	}
}

// WithEnvOptions adds declarations, libraries, and other CEL environment options in order.
func WithEnvOptions(options ...cel.EnvOption) Option {
	return func(settings *engineOptions) {
		settings.envOptions = append(settings.envOptions, options...)
	}
}

// Engine owns a CEL environment. Consumers supply their own declarations and libraries.
type Engine struct {
	env                *cel.Env
	programs           *ttlcache.Cache[string, cachedProgram]
	maxExpressionSize  int
	maxExpressionNodes int
	maxExpressionCost  uint64
}

type cachedProgram struct {
	program *Program
	err     error
}

// Program is a checked expression with its declared output type.
type Program struct {
	program    cel.Program
	outputType *cel.Type
	expression ast.Expr
}

// New constructs an engine with optional settings and CEL environment options.
func New(options ...Option) (*Engine, error) {
	var settings engineOptions
	for _, option := range options {
		option(&settings)
	}
	config := settings.config
	if config.MaxExpressionSize < 0 {
		return nil, errors.New("maximum CEL expression size must be non-negative")
	}
	if config.MaxExpressionNodes < 0 {
		return nil, errors.New("maximum CEL expression node count must be non-negative")
	}
	if config.CacheTTL < 0 {
		return nil, errors.New("CEL cache TTL must be non-negative")
	}
	maxExpressionSize := cmp.Or(config.MaxExpressionSize, 1_000)
	envOptions := append(settings.envOptions, cel.ParserExpressionSizeLimit(maxExpressionSize))
	env, err := cel.NewEnv(envOptions...)
	if err != nil {
		return nil, err
	}
	return &Engine{
		env:                env,
		maxExpressionSize:  maxExpressionSize,
		maxExpressionNodes: config.MaxExpressionNodes,
		maxExpressionCost:  cmp.Or(config.MaxExpressionCost, 10_000),
		programs: ttlcache.New(
			ttlcache.WithCapacity[string, cachedProgram](cmp.Or(config.CacheCapacity, 256)),
			ttlcache.WithTTL[string, cachedProgram](cmp.Or(config.CacheTTL, ttlcache.NoTTL)),
		),
	}, nil
}

// Compile parses, type-checks, and builds an expression for this environment.
// Successful compilations return the same Program while cached. Eviction does
// not invalidate a Program already held by a caller. Compilation errors within
// the expression size limit are cached too. Compile is safe to call concurrently.
func (e *Engine) Compile(expression string) (*Program, error) {
	if len(expression) > e.maxExpressionSize {
		size := 0
		for range expression {
			if size == e.maxExpressionSize {
				return nil, fmt.Errorf("parsing CEL expression: exceeds the limit of %d Unicode code points", e.maxExpressionSize)
			}
			size++
		}
	}
	if item := e.programs.Get(expression); item != nil {
		cached := item.Value()
		return cached.program, cached.err
	}

	// The caller may have sliced the expression from a much larger buffer.
	expression = strings.Clone(expression)
	program, err := e.compile(expression)
	item, _ := e.programs.GetOrSet(expression, cachedProgram{program: program, err: err})
	cached := item.Value()
	return cached.program, cached.err
}

func (e *Engine) compile(expression string) (*Program, error) {
	parsed, issues := e.env.ParseSource(common.NewTextSource(expression))
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("parsing CEL expression: %w", issues.Err())
	}
	if e.maxExpressionNodes > 0 {
		nodes := 0
		ast.PreOrderVisit(parsed.NativeRep().Expr(), ast.NewExprVisitor(func(ast.Expr) {
			nodes++
		}))
		if nodes > e.maxExpressionNodes {
			return nil, fmt.Errorf("compiling CEL expression: exceeds the limit of %d expression nodes", e.maxExpressionNodes)
		}
	}
	checked, issues := e.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("checking CEL expression: %w", issues.Err())
	}
	program, err := e.env.Program(checked, cel.CostLimit(e.maxExpressionCost))
	if err != nil {
		return nil, fmt.Errorf("building CEL program: %w", err)
	}
	return &Program{
		program:    program,
		outputType: checked.OutputType(),
		expression: checked.NativeRep().Expr(),
	}, nil
}

// OutputType returns the checked CEL result type for consumer-specific validation.
func (p *Program) OutputType() *cel.Type {
	return p.outputType
}

// CheckedExpression returns a copy of the checked expression for static analysis.
func (p *Program) CheckedExpression() ast.Expr {
	return ast.NewExprFactory().CopyExpr(p.expression)
}

// Evaluate returns the CEL value without consumer-specific conversion.
// Concurrent evaluations require independent activations or activations that
// support concurrent use. Resolve shared lazy globals before concurrent use or
// provide a concurrency-safe global activation.
func (p *Program) Evaluate(activation any) (ref.Val, error) {
	bindings, isMap := activation.(map[string]any)
	if activation == nil || (isMap && bindings == nil) {
		return nil, errors.New("activation is nil")
	}
	value, _, err := p.program.Eval(activation)
	if err != nil {
		return nil, fmt.Errorf("evaluating CEL expression: %w", err)
	}
	return value, nil
}

// Activate binds caller-selected roots. Values already adapted to CEL are used
// directly. Other roots are normalized through JSON.
func Activate(roots map[string]any) (map[string]any, error) {
	typedRoots := make(map[string]any)
	for name, root := range roots {
		if value, ok := root.(ref.Val); ok {
			typedRoots[name] = value
		}
	}
	if len(typedRoots) > 0 {
		if len(typedRoots) == len(roots) {
			return typedRoots, nil
		}
		roots = maps.Clone(roots)
		for name := range typedRoots {
			delete(roots, name)
		}
	}
	data, err := json.Marshal(roots)
	if err != nil {
		return nil, fmt.Errorf("marshaling activation roots: %w", err)
	}
	var activation map[string]any
	if err := json.Unmarshal(data, &activation); err != nil {
		return nil, fmt.Errorf("unmarshaling activation roots: %w", err)
	}
	maps.Copy(activation, typedRoots)
	return activation, nil
}

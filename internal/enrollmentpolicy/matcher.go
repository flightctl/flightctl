// Package enrollmentpolicy evaluates CEL expressions over reported enrollment system information.
package enrollmentpolicy

import (
	"errors"
	"fmt"
	"reflect"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"github.com/flightctl/flightctl/internal/celruntime"
	"github.com/flightctl/flightctl/internal/domain"
)

const maxReportedStringBytes = 4096

// ErrEvaluation indicates that CEL could not evaluate the reported system information.
var ErrEvaluation = errors.New("CEL evaluation failed. Check that the request reports the referenced system information")

// ErrReportedStringTooLong indicates that a reported string read by CEL exceeds the input limit.
var ErrReportedStringTooLong = fmt.Errorf("a reported system information string exceeds %d bytes", maxReportedStringBytes)

// Matcher compiles enrollment expressions in a dedicated CEL environment.
type Matcher struct {
	engine *celruntime.Engine
}

// Policy is a compiled enrollment match expression.
type Policy struct {
	program *celruntime.Program
	fields  map[string]struct{}
}

// NewMatcher constructs a matcher with the restricted enrollment declarations.
func NewMatcher() (*Matcher, error) {
	engine, err := celruntime.New(
		celruntime.WithConfig(celruntime.Config{MaxExpressionNodes: 256}),
		celruntime.WithEnvOptions(
			cel.Variable(systemInfoRoot, cel.MapType(cel.StringType, cel.DynType)),
			cel.ClearMacros(),
			cel.ASTValidators(expressionValidator{}),
		),
	)
	if err != nil {
		return nil, err
	}
	return &Matcher{engine: engine}, nil
}

// Compile validates a policy expression synchronously.
func (m *Matcher) Compile(expression string) (*Policy, error) {
	program, err := m.engine.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("spec.matchExpression: %w", err)
	}
	return &Policy{program: program, fields: referencedFields(program.CheckedExpression())}, nil
}

// Match evaluates the policy against the reported system information.
func (p *Policy) Match(spec domain.EnrollmentRequestSpec) (bool, error) {
	if p == nil || p.program == nil {
		return false, errors.New("CEL expression has not been compiled")
	}
	adapter := &reportAdapter{}
	activation, err := celruntime.Activate(map[string]any{
		systemInfoRoot: types.NewStringInterfaceMap(adapter, p.reportedFields(spec.DeviceStatus)),
	})
	if err != nil {
		return false, ErrEvaluation
	}
	value, err := p.program.Evaluate(activation)
	if adapter.oversized {
		return false, ErrReportedStringTooLong
	}
	if err != nil {
		return false, ErrEvaluation
	}
	result, ok := value.(types.Bool)
	if !ok {
		return false, ErrEvaluation
	}
	return bool(result), nil
}

func (p *Policy) reportedFields(status *domain.DeviceStatus) map[string]any {
	fields := make(map[string]any)
	if status != nil && len(p.fields) > 0 {
		info := status.SystemInfo
		fields = reportObject(reflect.ValueOf(info), p.fields)
		for field := range p.fields {
			if value, present := info.Get(field); present {
				fields[field] = value
			}
		}
	}
	return fields
}

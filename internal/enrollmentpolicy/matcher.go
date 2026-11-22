// Package enrollmentpolicy implements the CEL contract for enrollment approval policies.
package enrollmentpolicy

import (
	"fmt"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"github.com/flightctl/flightctl/internal/celruntime"
	"github.com/flightctl/flightctl/internal/domain"
)

const (
	productSerial = "systemInfo.productSerial"
	productUUID   = "systemInfo.productUuid"
	customInfo    = "systemInfo.customInfo"
)

// Matcher compiles enrollment expressions in a dedicated CEL environment.
type Matcher struct {
	engine *celruntime.Engine
}

// Policy is a compiled enrollment match expression.
type Policy struct {
	program *celruntime.Program
}

// NewMatcher constructs a matcher with the restricted enrollment declarations.
func NewMatcher() (*Matcher, error) {
	engine, err := celruntime.New(celruntime.WithEnvOptions(
		cel.Variable(productSerial, cel.StringType),
		cel.Variable(productUUID, cel.StringType),
		cel.Variable(customInfo, cel.MapType(cel.StringType, cel.StringType)),
		cel.ClearMacros(),
		cel.ASTValidators(expressionValidator{}),
	))
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
	return &Policy{program: program}, nil
}

// Match evaluates the policy against the reported system identifiers.
func (p *Policy) Match(spec domain.EnrollmentRequestSpec) (bool, error) {
	roots := map[string]any{customInfo: map[string]string{}}
	if spec.DeviceStatus != nil {
		info := spec.DeviceStatus.SystemInfo
		if serial, ok := info.AdditionalProperties["productSerial"]; ok {
			roots[productSerial] = serial
		}
		if uuid, ok := info.AdditionalProperties["productUuid"]; ok {
			roots[productUUID] = uuid
		}
		if info.CustomInfo != nil && *info.CustomInfo != nil {
			roots[customInfo] = *info.CustomInfo
		}
	}
	activation, err := celruntime.Activate(roots)
	if err != nil {
		return false, err
	}
	value, err := p.program.Evaluate(activation)
	if err != nil {
		return false, err
	}
	return value == types.True, nil
}

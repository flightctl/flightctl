// Package enrollmentpolicy evaluates CEL expressions over reported enrollment identifiers.
package enrollmentpolicy

import (
	"errors"
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

	maxIdentifierBytes = 4096
)

// ErrEvaluation indicates that CEL could not evaluate the reported identifiers.
var ErrEvaluation = errors.New("CEL evaluation failed. Check that the request reports the referenced system identifiers")

// ErrIdentifierTooLong indicates that a referenced identifier exceeds the input limit.
var ErrIdentifierTooLong = fmt.Errorf("a referenced system identifier exceeds %d bytes", maxIdentifierBytes)

// Matcher compiles enrollment expressions in a dedicated CEL environment.
type Matcher struct {
	engine *celruntime.Engine
}

// Policy is a compiled enrollment match expression.
type Policy struct {
	program     *celruntime.Program
	identifiers map[identifier]struct{}
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
	return &Policy{program: program, identifiers: referencedIdentifiers(program.CheckedExpression())}, nil
}

// Match evaluates the policy against the reported system identifiers.
func (p *Policy) Match(spec domain.EnrollmentRequestSpec) (bool, error) {
	if p == nil || p.program == nil {
		return false, errors.New("CEL expression has not been compiled")
	}
	activation, err := p.activate(spec.DeviceStatus)
	if err != nil {
		return false, err
	}
	value, err := p.program.Evaluate(activation)
	if err != nil {
		return false, ErrEvaluation
	}
	result, ok := value.(types.Bool)
	if !ok {
		return false, ErrEvaluation
	}
	return bool(result), nil
}

func (p *Policy) activate(status *domain.DeviceStatus) (map[string]any, error) {
	custom := make(map[string]string)
	roots := map[string]any{customInfo: custom}
	if status == nil {
		return roots, nil
	}

	// Project only identifiers used in the expression. Reported custom info may
	// contain an arbitrary number of unrelated keys.
	for field := range p.identifiers {
		value, ok := field.reportedValue(status.SystemInfo)
		if !ok {
			continue
		}
		if len(value) > maxIdentifierBytes {
			return nil, ErrIdentifierTooLong
		}
		if field.field == customInfo {
			custom[field.key] = value
		} else {
			roots[field.field] = value
		}
	}

	// This activation contains only native strings and maps of strings.
	activation, err := celruntime.Activate(roots)
	if err != nil {
		return nil, ErrEvaluation
	}
	return activation, nil
}

func (id identifier) reportedValue(info domain.DeviceSystemInfo) (string, bool) {
	switch id.field {
	case productSerial:
		return info.Get("productSerial")
	case productUUID:
		return info.Get("productUuid")
	case customInfo:
		if info.CustomInfo != nil {
			value, ok := (*info.CustomInfo)[id.key]
			return value, ok
		}
	}
	return "", false
}

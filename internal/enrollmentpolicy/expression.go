package enrollmentpolicy

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
)

type expressionValidator struct{}

func (expressionValidator) Name() string {
	return "flightctl.enrollmentpolicy"
}

func (expressionValidator) Validate(_ *cel.Env, _ cel.ValidatorConfig, checked *ast.AST, issues *cel.Issues) {
	expression := checked.Expr()
	if checked.GetType(expression.ID()) != cel.BoolType {
		issues.ReportErrorAtID(expression.ID(), "expression must return a boolean")
		return
	}
	if !supportedExpression(expression) {
		issues.ReportErrorAtID(expression.ID(), "expected boolean literals or identifier comparisons using ==, in, and &&")
	}
}

func supportedExpression(expression ast.Expr) bool {
	if expression.Kind() == ast.LiteralKind {
		_, ok := expression.AsLiteral().(types.Bool)
		return ok
	}
	if expression.Kind() != ast.CallKind {
		return false
	}
	call := expression.AsCall()
	args := call.Args()
	if call.IsMemberFunction() || len(args) != 2 {
		return false
	}
	if call.FunctionName() == operators.LogicalAnd {
		return supportedExpression(args[0]) && supportedExpression(args[1])
	}
	return supportedIdentifier(args[0]) && supportedComparisonValue(call.FunctionName(), args[1])
}

func supportedComparisonValue(operator string, expression ast.Expr) bool {
	switch operator {
	case operators.Equals:
		return isStringLiteral(expression)
	case operators.In:
		if expression.Kind() != ast.ListKind {
			return false
		}
		for _, element := range expression.AsList().Elements() {
			if !isStringLiteral(element) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func supportedIdentifier(expression ast.Expr) bool {
	switch expression.Kind() {
	case ast.IdentKind:
		name := expression.AsIdent()
		return name == productSerial || name == productUUID
	case ast.CallKind:
		call := expression.AsCall()
		args := call.Args()
		return call.FunctionName() == operators.Index && !call.IsMemberFunction() && len(args) == 2 &&
			args[0].Kind() == ast.IdentKind && args[0].AsIdent() == customInfo && isStringLiteral(args[1])
	default:
		return false
	}
}

func isStringLiteral(expression ast.Expr) bool {
	if expression.Kind() != ast.LiteralKind {
		return false
	}
	_, ok := expression.AsLiteral().(types.String)
	return ok
}

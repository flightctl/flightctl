package enrollmentpolicy

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
)

// identifier distinguishes the built-in fields from keys in the custom map.
type identifier struct {
	field string
	key   string
}

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
	_, ok := readIdentifier(args[0])
	return ok && supportedComparisonValue(call.FunctionName(), args[1])
}

func supportedComparisonValue(operator string, expression ast.Expr) bool {
	switch operator {
	case operators.Equals:
		_, ok := stringLiteral(expression)
		return ok
	case operators.In:
		if expression.Kind() != ast.ListKind {
			return false
		}
		for _, element := range expression.AsList().Elements() {
			if _, ok := stringLiteral(element); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func referencedIdentifiers(expression ast.Expr) map[identifier]struct{} {
	identifiers := make(map[identifier]struct{})
	ast.PreOrderVisit(expression, ast.NewExprVisitor(func(expr ast.Expr) {
		if field, ok := readIdentifier(expr); ok {
			identifiers[field] = struct{}{}
		}
	}))
	return identifiers
}

func readIdentifier(expression ast.Expr) (identifier, bool) {
	switch expression.Kind() {
	case ast.IdentKind:
		name := expression.AsIdent()
		if name == productSerial || name == productUUID {
			return identifier{field: name}, true
		}
	case ast.CallKind:
		call := expression.AsCall()
		args := call.Args()
		if call.FunctionName() == operators.Index && !call.IsMemberFunction() && len(args) == 2 &&
			args[0].Kind() == ast.IdentKind && args[0].AsIdent() == customInfo {
			key, ok := stringLiteral(args[1])
			return identifier{field: customInfo, key: key}, ok
		}
	}
	return identifier{}, false
}

func stringLiteral(expression ast.Expr) (string, bool) {
	if expression.Kind() != ast.LiteralKind {
		return "", false
	}
	value, ok := expression.AsLiteral().(types.String)
	return string(value), ok
}

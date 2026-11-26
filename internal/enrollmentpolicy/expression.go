package enrollmentpolicy

import (
	"reflect"

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
		issues.ReportErrorAtID(expression.ID(), "expected boolean literals or field comparisons using ==, in, and && with matching value types")
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
	_, kind := reportedPath(args[0])
	if kind == nil {
		return false
	}
	switch call.FunctionName() {
	case operators.Equals:
		return compatibleLiteral(args[1], kind)
	case operators.In:
		return args[1].Kind() == ast.ListKind && compatibleElements(args[1], kind)
	default:
		return false
	}
}

// reportedPath resolves literal selections to their first field under
// systemInfo and the API type at the end of the path.
func reportedPath(expression ast.Expr) (string, reflect.Type) {
	switch expression.Kind() {
	case ast.SelectKind:
		selection := expression.AsSelect()
		if !selection.IsTestOnly() {
			return selectReportedField(selection.Operand(), selection.FieldName())
		}
	case ast.CallKind:
		call := expression.AsCall()
		args := call.Args()
		if call.IsMemberFunction() || call.FunctionName() != operators.Index || len(args) != 2 || args[1].Kind() != ast.LiteralKind {
			return "", nil
		}
		switch value := args[1].AsLiteral().(type) {
		case types.String:
			return selectReportedField(args[0], string(value))
		case types.Int:
			field, list := reportedPath(args[0])
			list = indirectType(list)
			if value >= 0 && list != nil && (list.Kind() == reflect.Slice || list.Kind() == reflect.Array) {
				return field, list.Elem()
			}
		}
	}
	return "", nil
}

func selectReportedField(object ast.Expr, key string) (string, reflect.Type) {
	if object.Kind() == ast.IdentKind && object.AsIdent() == systemInfoRoot {
		return key, reportFieldType(systemInfoType, key)
	}
	root, kind := reportedPath(object)
	return root, reportFieldType(kind, key)
}

func referencedFields(expression ast.Expr) map[string]struct{} {
	fields := make(map[string]struct{})
	ast.PreOrderVisit(expression, ast.NewExprVisitor(func(expr ast.Expr) {
		if field, kind := reportedPath(expr); kind != nil {
			fields[field] = struct{}{}
		}
	}))
	return fields
}

func compatibleLiteral(expression ast.Expr, valueType reflect.Type) bool {
	kind := indirectType(valueType)
	switch expression.Kind() {
	case ast.LiteralKind:
		switch expression.AsLiteral().(type) {
		case types.String:
			return kind.Kind() == reflect.String
		case types.Bool:
			return kind.Kind() == reflect.Bool
		case types.Int:
			return kind.Kind() >= reflect.Int && kind.Kind() <= reflect.Int64
		case types.Uint:
			return kind.Kind() >= reflect.Uint && kind.Kind() <= reflect.Uint64
		case types.Double:
			return kind.Kind() == reflect.Float32 || kind.Kind() == reflect.Float64
		case types.Null:
			return valueType.Kind() == reflect.Pointer || kind.Kind() == reflect.Map || kind.Kind() == reflect.Slice
		}
	case ast.ListKind:
		return (kind.Kind() == reflect.Slice || kind.Kind() == reflect.Array) && compatibleElements(expression, kind.Elem())
	case ast.MapKind:
		return (kind.Kind() == reflect.Map || kind.Kind() == reflect.Struct) && compatibleFields(expression, kind)
	}
	return false
}

func compatibleElements(expression ast.Expr, elementType reflect.Type) bool {
	for _, element := range expression.AsList().Elements() {
		if !compatibleLiteral(element, elementType) {
			return false
		}
	}
	return true
}

func compatibleFields(expression ast.Expr, objectType reflect.Type) bool {
	for _, entry := range expression.AsMap().Entries() {
		pair := entry.AsMapEntry()
		if pair.Key().Kind() != ast.LiteralKind {
			return false
		}
		key, ok := pair.Key().AsLiteral().(types.String)
		if !ok {
			return false
		}
		fieldType := reportFieldType(objectType, string(key))
		if fieldType == nil || !compatibleLiteral(pair.Value(), fieldType) {
			return false
		}
	}
	return true
}

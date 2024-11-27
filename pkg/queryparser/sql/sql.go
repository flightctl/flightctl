package sql

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/pkg/queryparser"
)

type FunctionHandler func(args ...any) (*FunctionResult, error)
type verificationHandler func(qf *queryparser.QueryFunc) error
type SQLParserOption func(*SQLParser) error

type FunctionResult struct {
	Args  []any
	Query string
}

type handler struct {
	usedBy        *queryparser.Set[string]
	Verifications []verificationHandler
	handle        FunctionHandler
}

type SQLParser struct {
	funcs     map[string]handler
	tokenizer queryparser.Tokenizer
}

func withPrecedingKeyQuery() verificationHandler {
	return func(queryFunc *queryparser.QueryFunc) error {
		args := queryFunc.Args()
		if len(args) == 0 {
			return fmt.Errorf("no arguments specified for function %s", queryFunc.Name())
		}

		if queryparser.IsValue(args[0]) {
			return fmt.Errorf("the first argument must be the function 'K' or 'CAST' for function %s", queryFunc.Name())
		}

		// Check if the first argument is a function 'K' or 'CAST'
		firstArgName := args[0].(*queryparser.QueryArgFunc).Value().Name()
		if firstArgName != "K" && firstArgName != "CAST" {
			return fmt.Errorf("the first argument must be the function 'K' or 'CAST' for function %s", queryFunc.Name())
		}

		if firstArgName == "CAST" {
			return withPrecedingKeyQuery()(args[0].(*queryparser.QueryArgFunc).Value())
		}

		return nil
	}
}

func withPrecedingKeyOrValueQuery() verificationHandler {
	return func(queryFunc *queryparser.QueryFunc) error {
		args := queryFunc.Args()
		if len(args) == 0 {
			return fmt.Errorf("no arguments specified for function %s", queryFunc.Name())
		}

		if queryparser.IsValue(args[0]) {
			return fmt.Errorf("the first argument must be the function 'K' or 'V' for function %s", queryFunc.Name())
		}

		// Check if the first argument is a function 'K' or 'V'
		firstArgName := args[0].(*queryparser.QueryArgFunc).Value().Name()
		if firstArgName != "K" && firstArgName != "V" {
			return fmt.Errorf("the first argument must be the function 'K' or 'V' for function %s", queryFunc.Name())
		}

		return nil
	}
}

func withNoValues() verificationHandler {
	return func(queryFunc *queryparser.QueryFunc) error {
		for _, arg := range queryFunc.Args() {
			if queryparser.IsValue(arg) {
				return fmt.Errorf("does not allow values")
			}
		}
		return nil
	}
}

// WithTokenizer sets a custom tokenizer for the SQLParser.
// This allows for overriding the default tokenization behavior with a user-provided tokenizer
func WithTokenizer(tokenizer queryparser.Tokenizer) SQLParserOption {
	return func(sp *SQLParser) error {
		sp.tokenizer = tokenizer
		return nil
	}
}

// WithOverrideFunction allows you to override an existing SQL function
// in the SQLParser with a custom implementation.
func WithOverrideFunction(name string, f FunctionHandler) SQLParserOption {
	return func(sp *SQLParser) error {
		h, exists := sp.funcs[name]
		if !exists {
			return fmt.Errorf("does not exist")
		}

		h.handle = f
		sp.funcs[name] = h
		return nil
	}
}

// Wrap wraps a FunctionHandler and ensures that its arguments are of type T.
func Wrap[T any](f func(args ...T) (*FunctionResult, error)) FunctionHandler {
	return func(args ...any) (*FunctionResult, error) {
		argsList, err := queryparser.AssertSliceType[T](args)
		if err != nil {
			return nil, err
		}
		return f(argsList...)
	}
}

// NewSQLParser initializes and returns a new instance of Parser.
//
// The SQLParser is configured with a set of predefined SQL functions
// that can be used to construct queries, including logical operations
// (AND, OR), comparison operators (EQ, NOTEQ, LT, LTE, GT, GTE), and
// other specialized functions (IN, NOTIN, LIKE, NOTLIKE, ISNULL, ISNOTNULL, CONTAINS, OVERLAPS, etc.).
func NewSQLParser(options ...SQLParserOption) (queryparser.Parser, error) {
	sp := &SQLParser{}
	sp.funcs = map[string]handler{
		"AND": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "OR"),
			Verifications: []verificationHandler{withNoValues()},
			handle:        Wrap(sp.queryAnd),
		},
		"OR": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND"),
			Verifications: []verificationHandler{withNoValues()},
			handle:        Wrap(sp.queryOr),
		},
		"EQ": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryEqual),
		},
		"NOTEQ": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryNotEqual),
		},
		"LT": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryLessThan),
		},
		"LTE": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryLessThanOrEqual),
		},
		"GT": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryGreaterThan),
		},
		"GTE": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryGreaterThanOrEqual),
		},
		"IN": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryIn),
		},
		"NOTIN": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryNotIn),
		},
		"LIKE": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryLike),
		},
		"NOTLIKE": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryNotLike),
		},
		"ISNULL": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryIsNull),
		},
		"ISNOTNULL": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryIsNotNull),
		},
		"CONTAINS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryContains),
		},
		"NOTCONTAINS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryNotContains),
		},
		"JSONB_CONTAINS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryJsonbContains),
		},
		"JSONB_NOTCONTAINS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryJsonbNotContains),
		},
		"OVERLAPS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryOverlaps),
		},
		"NOTOVERLAPS": {
			usedBy:        queryparser.NewSet[string]().Add(queryparser.RootFunc, "AND", "OR"),
			Verifications: []verificationHandler{withPrecedingKeyQuery(), withNoValues()},
			handle:        Wrap(sp.queryNotOverlaps),
		},
		"CAST": {
			usedBy: queryparser.NewSet[string]().Add("EQ", "NOTEQ", "LT", "LTE", "GT", "GTE", "IN", "NOTIN", "LIKE",
				"NOTLIKE", "OVERLAPS", "NOTOVERLAPS", "CONTAINS", "NOTCONTAINS", "JSONB_CONTAINS", "JSONB_NOTCONTAINS", "ISNULL", "ISNOTNULL"),
			Verifications: []verificationHandler{withPrecedingKeyOrValueQuery()},
			handle:        sp.queryCast,
		},
		"K": {
			usedBy: queryparser.NewSet[string]().Add("EQ", "NOTEQ", "LT", "LTE", "GT", "GTE", "IN", "NOTIN", "LIKE",
				"NOTLIKE", "OVERLAPS", "NOTOVERLAPS", "CONTAINS", "NOTCONTAINS", "JSONB_CONTAINS", "JSONB_NOTCONTAINS", "ISNULL", "ISNOTNULL", "CAST"),
			handle: Wrap(sp.queryKey),
		},
		"V": {
			usedBy: queryparser.NewSet[string]().Add("EQ", "NOTEQ", "LT", "LTE", "GT", "GTE", "IN", "NOTIN", "LIKE",
				"NOTLIKE", "OVERLAPS", "NOTOVERLAPS", "CONTAINS", "NOTCONTAINS", "JSONB_CONTAINS", "JSONB_NOTCONTAINS", "CAST"),
			handle: sp.queryValue,
		},
	}

	for _, option := range options {
		if err := option(sp); err != nil {
			return nil, err
		}
	}
	return sp, nil
}

type parser struct {
	sqlparser *SQLParser
}

// Parse constructs a SQL query based on the provided input.
// This method tokenizes the input, verifies the structure of the tokens,
// and executes the corresponding SQL functions to generate the final query
// string along with its parameters.
func (sp *SQLParser) Parse(ctx context.Context, input any, params ...string) (string, []any, error) {
	if input == nil {
		return "", nil, nil
	}

	p := &parser{
		sqlparser: sp,
	}

	qfuncs := make(queryparser.QueryFuncSet, len(sp.funcs))
	for f, h := range sp.funcs {
		qfuncs[f] = queryparser.QueryFuncHandler{
			Invoke: p.dispatcher,
			UsedBy: h.usedBy,
		}
	}
	parserOptions := []queryparser.ParserOption{queryparser.WithFunctions(qfuncs), queryparser.WithParams(params)}
	if sp.tokenizer != nil {
		parserOptions = append(parserOptions, queryparser.WithTokenizer(sp.tokenizer))
	}

	root, err := queryparser.Parse(ctx, input, parserOptions...)
	if err != nil {
		return "", nil, err
	}

	if len(root.Args()) == 0 {
		return "", nil, nil
	}

	if queryparser.IsValue(root.Args()[0]) {
		return "", nil, fmt.Errorf("unexpected value without a function")
	}

	f := root.Args()[0].(*queryparser.QueryArgFunc)
	fr := f.Value().Result().(*FunctionResult)
	return fr.Query, fr.Args, nil
}

func (p *parser) dispatcher(qf *queryparser.QueryFunc) error {
	fn := qf.Name()

	sqlf, exists := p.sqlparser.funcs[fn]
	if !exists {
		return fmt.Errorf("function is undefined")
	}

	for _, verify := range sqlf.Verifications {
		if err := verify(qf); err != nil {
			return fmt.Errorf("failed verification: %w", err)
		}
	}

	var funcArgs, retArgs []any
	for _, arg := range qf.Args() {
		if queryparser.IsValue(arg) {
			funcArgs = append(funcArgs, arg.(*queryparser.QueryArgValue).Value())
		} else {
			qfRet := arg.(*queryparser.QueryArgFunc).Value().Result().(*FunctionResult)
			funcArgs = append(funcArgs, qfRet.Query)
			retArgs = append(retArgs, qfRet.Args...)
		}
	}

	res, err := sqlf.handle(funcArgs...)
	if err != nil {
		return err
	}

	if res == nil {
		return fmt.Errorf("function returned a nil result")
	}

	ret := &FunctionResult{
		Query: res.Query,
		Args:  append(res.Args, retArgs...),
	}

	qf.SetResult(ret)
	return nil
}

func (sp *SQLParser) queryAnd(queries ...string) (*FunctionResult, error) {
	if err := validateArgsCount(queries, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("(%s)", strings.Join(queries, " AND ")),
	}, nil
}

func (sp *SQLParser) queryOr(queries ...string) (*FunctionResult, error) {
	if err := validateArgsCount(queries, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("(%s)", strings.Join(queries, " OR ")),
	}, nil
}

func (sp *SQLParser) queryEqual(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s = %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryNotEqual(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s != %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryLessThan(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s < %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryLessThanOrEqual(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s <= %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryGreaterThan(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s > %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryGreaterThanOrEqual(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s >= %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryIn(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s IN (%s)", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

func (sp *SQLParser) queryNotIn(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s NOT IN (%s)", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

func (sp *SQLParser) queryLike(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s LIKE %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryNotLike(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s NOT LIKE %s", args[0], args[1]),
	}, nil
}

func (sp *SQLParser) queryIsNull(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 1, 1); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s IS NULL", args[0]),
	}, nil
}

func (sp *SQLParser) queryIsNotNull(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 1, 1); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s IS NOT NULL", args[0]),
	}, nil
}

func (sp *SQLParser) queryContains(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s @> ARRAY[%s]", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

func (sp *SQLParser) queryNotContains(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("NOT (%s @> ARRAY[%s])", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

func (sp *SQLParser) queryJsonbContains(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	containsQuery := fmt.Sprintf("%s @> %s", args[0], args[1])
	return &FunctionResult{
		Query: containsQuery,
	}, nil
}

func (sp *SQLParser) queryJsonbNotContains(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	notContainsQuery := fmt.Sprintf("NOT (%s @> %s)", args[0], args[1])
	return &FunctionResult{
		Query: notContainsQuery,
	}, nil
}

func (sp *SQLParser) queryOverlaps(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("%s && ARRAY[%s]", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

func (sp *SQLParser) queryNotOverlaps(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Query: fmt.Sprintf("NOT (%s && ARRAY[%s])", args[0], strings.Join(args[1:], ", ")),
	}, nil
}

var validTypeRegex = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func (sp *SQLParser) queryCast(args ...any) (*FunctionResult, error) {
	if err := validateArgsCount(args, 2, 2); err != nil {
		return nil, err
	}

	v, err := queryparser.AssertType[string](args[0])
	if err != nil {
		return nil, fmt.Errorf("value must be of type string for function CAST")
	}

	t, err := queryparser.AssertType[string](args[1])
	if err != nil {
		return nil, fmt.Errorf("invalid type provided")
	}

	if !validTypeRegex.MatchString(t) {
		return nil, fmt.Errorf("invalid type provided")
	}

	return &FunctionResult{
		Query: fmt.Sprintf("CAST(%s AS %s)", v, t),
	}, nil
}

func (sp *SQLParser) queryKey(args ...string) (*FunctionResult, error) {
	if err := validateArgsCount(args, 1, 1); err != nil {
		return nil, err
	}

	key := args[0]
	if !isValidColumnName(key) {
		return nil, fmt.Errorf("invalid column name: %s", key)
	}

	return &FunctionResult{
		Query: key,
	}, nil
}

func (sp *SQLParser) queryValue(args ...any) (*FunctionResult, error) {
	if err := validateArgsCount(args, 1, 1); err != nil {
		return nil, err
	}

	return &FunctionResult{
		Args:  []any{args[0]},
		Query: "?",
	}, nil
}

var columnRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func isValidColumnName(name string) bool {
	return name != "" && columnRegex.MatchString(name)
}

func validateArgsCount[T any](args []T, opts ...int) error {
	min, max := 0, math.MaxInt
	if len(opts) > 0 {
		min = opts[0]
	}
	if len(opts) > 1 {
		max = opts[1]
	}

	if len(args) < min {
		return fmt.Errorf("function requires at least %d arguments", min)
	}
	if len(args) > max {
		return fmt.Errorf("function accepts up to %d arguments", max)
	}
	return nil
}

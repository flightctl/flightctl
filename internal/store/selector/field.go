package selector

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sql"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/selection"
)

type fieldSelector struct {
	parser        queryparser.Parser
	fieldResolver *selectorFieldResolver
}

func NewFieldSelector(dest any) (*fieldSelector, error) {
	fs := &fieldSelector{}

	var err error
	fs.fieldResolver, err = SelectorFieldResolver(dest)
	if err != nil {
		return nil, err
	}

	fs.parser, err = sql.NewSQLParser(
		sql.WithTokenizer(fs),
		sql.WithOverrideFunction("K", sql.Wrap(fs.queryField)),
	)
	if err != nil {
		return nil, err
	}

	return fs, nil
}

// ParseFromString parses the selector string and returns a SQL query with parameters.
func (fs *fieldSelector) ParseFromString(ctx context.Context, input string) (string, []any, error) {
	selector, err := fields.ParseSelector(input)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax, err)
	}

	q, args, err := fs.parser.Parse(ctx, selector)
	if err != nil {
		if se, ok := IsSelectorError(err); ok {
			return "", nil, se
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

// Parse parses the selector and returns a SQL query with parameters.
func (fs *fieldSelector) Parse(ctx context.Context, selector fields.Selector) (string, []any, error) {
	q, args, err := fs.parser.Parse(ctx, selector)
	if err != nil {
		if se, ok := IsSelectorError(err); ok {
			return "", nil, se
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (fs *fieldSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == nil {
		return nil, nil
	}

	// Assert that input is a selector
	selector, ok := input.(fields.Selector)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected fieldSelector, got %T", input)
	}

	requirements := selector.Requirements()
	tokens := make(queryparser.TokenSet, 0)

	for _, req := range requirements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		field, value, operator := SelectorFieldName(strings.TrimSpace(req.Field)), req.Value, req.Operator
		resolvedFields, err := fs.fieldResolver.ResolveFields(field)
		if err != nil {
			return nil, err
		}

		resolvedTokens := queryparser.NewTokenSet()
		for _, resolvedField := range resolvedFields {
			fieldToken, err := fs.createFieldToken(resolvedField)
			if err != nil {
				return nil, err
			}

			var valueToken queryparser.TokenSet
			if value != "" {
				valueToken = queryparser.NewTokenSet()
				vtokens, err := fs.createValueToken(resolvedField, value)
				if err != nil {
					return nil, err
				}
				valueToken = valueToken.Append(vtokens)
			}

			operatorToken, err := fs.createOperatorToken(operator, resolvedField, fieldToken, valueToken)
			if err != nil {
				return nil, err
			}

			resolvedTokens = resolvedTokens.Append(operatorToken)
		}

		// If multiple fields are resolved, wrap them in an OR token
		if len(resolvedFields) > 1 {
			tokens = tokens.AddFunctionToken("OR", func() queryparser.TokenSet {
				return resolvedTokens
			})
		} else {
			tokens = tokens.Append(resolvedTokens)
		}
	}

	if len(requirements) > 1 {
		tokens = queryparser.NewTokenSet(len(tokens)+2).AddFunctionToken("AND", func() queryparser.TokenSet {
			return tokens
		})
	}

	return tokens, nil
}

type resolverFunc[T any] func(T) queryparser.TokenSet

func (fs *fieldSelector) createFieldToken(selectorField *SelectorField) (queryparser.TokenSet, error) {
	return fs.resolveField(selectorField, func(f string) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(f)
		})
	})
}

func (fs *fieldSelector) createValueToken(selectorField *SelectorField, value string) (queryparser.TokenSet, error) {
	return fs.resolveValue(selectorField, value, func(v any) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(v)
		})
	})
}

func (fs *fieldSelector) createOperatorToken(operator selection.Operator, selectorField *SelectorField, fieldToken, valueToken queryparser.TokenSet) (queryparser.TokenSet, error) {
	return fs.resolveQuery(operator, selectorField, func(op string) queryparser.TokenSet {
		switch operator {
		case selection.Exists, selection.DoesNotExist:
			// Avoid using JSONB casting in default case
			return queryparser.NewTokenSet().AddFunctionToken(op, func() queryparser.TokenSet {
				return queryparser.NewTokenSet().Append(fieldToken, valueToken)
			})
		case selection.NotEquals, selection.NotIn:
			return queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
				return queryparser.NewTokenSet().AddFunctionToken("ISNULL", func() queryparser.TokenSet { return fieldToken }).
					AddFunctionToken(op, func() queryparser.TokenSet {
						if selectorField.DataType == "jsonb" && selectorField.Type != Jsonb {
							return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
								return queryparser.NewTokenSet().Append(fieldToken).AddValueToken(selectorField.Type.String())
							}).Append(valueToken)
						}
						return queryparser.NewTokenSet().Append(fieldToken, valueToken)
					})
			})
		default:
			return queryparser.NewTokenSet().AddFunctionToken(op, func() queryparser.TokenSet {
				if selectorField.DataType == "jsonb" && selectorField.Type != Jsonb {
					return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
						return queryparser.NewTokenSet().Append(fieldToken).AddValueToken(selectorField.Type.String())
					}).Append(valueToken)
				}
				return queryparser.NewTokenSet().Append(fieldToken, valueToken)
			})
		}
	})
}

func (fs *fieldSelector) resolveQuery(operator selection.Operator, selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	op, exists := operatorsMap[operator]
	if !exists {
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("unknown operator %q", operator))
	}

	switch selectorField.Type {
	case Int, Float, SmallInt, BigInt:
		return fs.applyNumbersOperator(operator, resolve)
	case Bool:
		return fs.applyBooleanOperator(operator, resolve)
	case Time:
		return fs.applyTimestampOperator(operator, resolve)
	case IntArray, SmallIntArray, BigIntArray, FloatArray, BoolArray, TimestampArray, TextArray:
		return fs.applyArrayOperator(operator, resolve)
	case String, Jsonb:
		return resolve(op), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("unsupported type %q for operator %q", selectorField.Type.String(), operator))
	}
}

func (fs *fieldSelector) resolveField(selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	return resolve(selectorField.DBName), nil
}

func (fs *fieldSelector) resolveValue(selectorField *SelectorField, value string, resolve resolverFunc[any]) (queryparser.TokenSet, error) {
	switch selectorField.Type {
	case Int, IntArray:
		v, err := strconv.Atoi(value)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case SmallInt, SmallIntArray:
		v, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		if v < math.MinInt16 || v > math.MaxInt16 {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, fmt.Errorf("value out of range for int16: %d", v))
		}
		return resolve(int16(v)), nil

	case BigInt, BigIntArray:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case Float, FloatArray:
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case Bool, BoolArray:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case Time, TimestampArray:
		v, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case String, TextArray, Jsonb:
		return resolve(value), nil

	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
			fmt.Errorf("unknown type"))
	}
}

// applyArrayOperator applies the appropriate operator for array fields.
func (fs *fieldSelector) applyArrayOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.In:
		return resolve("OVERLAPS"), nil
	case selection.NotIn:
		return resolve("NOTOVERLAPS"), nil
	case selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for type array", operator))
	}
}

// applyTimestampOperator applies the appropriate operator for timestamp fields.
func (fs *fieldSelector) applyTimestampOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.GreaterThan, selection.LessThan,
		selection.In, selection.NotIn, selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for type timestamp", operator))
	}
}

// applyNumbersOperator applies the appropriate operator for numbers fields.
func (fs *fieldSelector) applyNumbersOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.GreaterThan, selection.LessThan,
		selection.In, selection.NotIn, selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for type number", operator))
	}
}

// applyBooleanOperator applies the appropriate operator for boolean fields.
func (fs *fieldSelector) applyBooleanOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.In, selection.NotIn,
		selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for type boolean", operator))
	}
}

var fieldRegex = regexp.MustCompile(`^[a-zA-Z0-9._]+$`)

func (fs *fieldSelector) queryField(args ...string) (*sql.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	field := strings.TrimSpace(args[0])
	if !fieldRegex.MatchString(field) {
		return nil, fmt.Errorf("the field name contains invalid characters")
	}

	return &sql.FunctionResult{
		Query: createParamsFromKey(field),
	}, nil
}

// createParamsFromKey constructs a SQL query parameter string from a dot-separated key.
// Intermediate parts are prefixed with the -> operator for JSONB, and the last part is prefixed with the ->> operator for JSONB fetching text.
func createParamsFromKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return ""
	}

	var params strings.Builder
	for i, part := range parts {
		if i == 0 {
			params.WriteString(part)
		} else if i == len(parts)-1 {
			params.WriteString(fmt.Sprintf(" ->> '%s'", part))
		} else {
			params.WriteString(fmt.Sprintf(" -> '%s'", part))
		}
	}

	return params.String()
}
